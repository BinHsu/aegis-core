// engine_cpp/src/vectordb/qdrant_client.cc

#include "engine_cpp/src/vectordb/qdrant_client.h"

#include <cstdlib>
#include <memory>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

#include "absl/status/status.h"
#include "absl/status/statusor.h"
#include "absl/strings/str_cat.h"
#include "absl/strings/strip.h"
// Message protos (from cc_proto_library) are exposed via Bazel's
// `_virtual_includes` symlink trees so bare include names work.
#include "collections.pb.h"
#include "json_with_int.pb.h"
#include "points.pb.h"
#include "qdrant_common.pb.h"
// Service protos (from cc_grpc_library) — the grpc rule does NOT
// populate _virtual_includes for its generated *.grpc.pb.h, so we
// include via the full _virtual_imports path rooted at bazel-bin.
// Ugly but stable: the path is deterministic from the proto_library
// target name + strip_import_prefix, and the headers' own sibling
// #includes use the same full-path form. See
// proto/qdrant/v1.17.1/BUILD.bazel for the per-service target split.
#include "grpcpp/channel.h"
#include "grpcpp/client_context.h"
#include "grpcpp/create_channel.h"
#include "grpcpp/security/credentials.h"
#include "grpcpp/support/status.h"
#include "proto/qdrant/v1.17.1/_virtual_imports/collections_service_proto/collections_service.grpc.pb.h"
#include "proto/qdrant/v1.17.1/_virtual_imports/points_service_proto/points_service.grpc.pb.h"

namespace aegis::vectordb {

namespace {

absl::Status FromGrpcStatus(const grpc::Status &s) {
  if (s.ok()) {
    return absl::OkStatus();
  }
  // Map a reasonable subset; unknown codes fall through as Internal so
  // callers never see absl::UnknownError with the wrong semantics.
  const std::string msg =
      absl::StrCat("qdrant gRPC error (", static_cast<int>(s.error_code()),
                   "): ", s.error_message());
  switch (s.error_code()) {
  case grpc::StatusCode::INVALID_ARGUMENT:
    return absl::InvalidArgumentError(msg);
  case grpc::StatusCode::NOT_FOUND:
    return absl::NotFoundError(msg);
  case grpc::StatusCode::ALREADY_EXISTS:
    return absl::AlreadyExistsError(msg);
  case grpc::StatusCode::FAILED_PRECONDITION:
    return absl::FailedPreconditionError(msg);
  case grpc::StatusCode::UNAUTHENTICATED:
    return absl::UnauthenticatedError(msg);
  case grpc::StatusCode::PERMISSION_DENIED:
    return absl::PermissionDeniedError(msg);
  case grpc::StatusCode::UNAVAILABLE:
    return absl::UnavailableError(msg);
  case grpc::StatusCode::DEADLINE_EXCEEDED:
    return absl::DeadlineExceededError(msg);
  default:
    return absl::InternalError(msg);
  }
}

qdrant::Distance ToQdrantDistance(DistanceMetric m) {
  switch (m) {
  case DistanceMetric::kCosine:
    return qdrant::Cosine;
  case DistanceMetric::kDotProduct:
    return qdrant::Dot;
  case DistanceMetric::kEuclidean:
    return qdrant::Euclid;
  }
  return qdrant::Cosine; // unreachable; enum is exhaustive above
}

// Wrap a payload map<string,string> into Qdrant's typed Value map.
void StringMapToQdrantPayload(
    const std::map<std::string, std::string> &in,
    ::google::protobuf::Map<std::string, qdrant::Value> *out) {
  for (const auto &[k, v] : in) {
    qdrant::Value value;
    value.set_string_value(v);
    (*out)[k] = std::move(value);
  }
}

// Inverse: pull string-typed values out of a Qdrant payload. Non-string
// values are rendered as their proto TextFormat — callers that need
// typed payloads should extend the API surface, not rely on coercion
// here.
std::map<std::string, std::string> QdrantPayloadToStringMap(
    const ::google::protobuf::Map<std::string, qdrant::Value> &in) {
  std::map<std::string, std::string> out;
  for (const auto &[k, v] : in) {
    if (v.kind_case() == qdrant::Value::kStringValue) {
      out[k] = v.string_value();
    } else {
      out[k] = v.ShortDebugString();
    }
  }
  return out;
}

void AddApiKeyIfSet(grpc::ClientContext &ctx, std::string_view api_key) {
  if (!api_key.empty()) {
    ctx.AddMetadata("api-key", std::string(api_key));
  }
}

} // namespace

struct QdrantClient::Impl {
  std::shared_ptr<grpc::Channel> channel;
  std::unique_ptr<qdrant::Collections::Stub> collections;
  std::unique_ptr<qdrant::Points::Stub> points;
  std::string api_key;
};

// -----------------------------------------------------------------------------
// Config / Create
// -----------------------------------------------------------------------------

absl::StatusOr<QdrantClient::Config> QdrantClient::ConfigFromEnv() {
  const char *url_env = std::getenv("QDRANT_URL");
  if (url_env == nullptr || *url_env == '\0') {
    return absl::InvalidArgumentError(
        "QdrantClient: QDRANT_URL env var is required but not set");
  }
  std::string url(url_env);

  Config cfg;
  // Strip scheme if present; infer TLS from https://.
  if (absl::StartsWith(url, "https://")) {
    cfg.use_tls = true;
    url = std::string(absl::StripPrefix(url, "https://"));
  } else if (absl::StartsWith(url, "http://")) {
    cfg.use_tls = false;
    url = std::string(absl::StripPrefix(url, "http://"));
  } else {
    cfg.use_tls = false; // bare host:port → assume local plaintext
  }
  cfg.endpoint = std::move(url);

  const char *key_env = std::getenv("QDRANT_API_KEY");
  if (key_env != nullptr) {
    cfg.api_key = key_env;
  }
  return cfg;
}

absl::StatusOr<std::unique_ptr<QdrantClient>>
QdrantClient::Create(const Config &cfg) {
  if (cfg.endpoint.empty()) {
    return absl::InvalidArgumentError(
        "QdrantClient::Create: Config.endpoint is empty");
  }
  auto creds = cfg.use_tls ? grpc::SslCredentials(grpc::SslCredentialsOptions())
                           : grpc::InsecureChannelCredentials();
  auto channel = grpc::CreateChannel(cfg.endpoint, creds);
  auto impl = std::make_unique<Impl>();
  impl->channel = channel;
  impl->collections = qdrant::Collections::NewStub(channel);
  impl->points = qdrant::Points::NewStub(channel);
  impl->api_key = cfg.api_key;
  return std::unique_ptr<QdrantClient>(new QdrantClient(std::move(impl)));
}

QdrantClient::QdrantClient(std::unique_ptr<Impl> impl)
    : impl_(std::move(impl)) {}

QdrantClient::~QdrantClient() = default;

// -----------------------------------------------------------------------------
// CreateCollection
// -----------------------------------------------------------------------------

absl::Status QdrantClient::CreateCollection(std::string_view name,
                                            int vector_dim,
                                            DistanceMetric metric) {
  if (name.empty()) {
    return absl::InvalidArgumentError(
        "QdrantClient::CreateCollection: name is empty");
  }
  if (vector_dim <= 0) {
    return absl::InvalidArgumentError(absl::StrCat(
        "QdrantClient::CreateCollection: vector_dim must be > 0, got ",
        vector_dim));
  }

  // Idempotency: check existence first. Qdrant's Create returns an
  // error if the collection exists, so we do an explicit CollectionExists
  // + (optional) compatibility check.
  {
    qdrant::CollectionExistsRequest req;
    req.set_collection_name(std::string(name));
    qdrant::CollectionExistsResponse resp;
    grpc::ClientContext ctx;
    AddApiKeyIfSet(ctx, impl_->api_key);
    const auto gstatus = impl_->collections->CollectionExists(&ctx, req, &resp);
    if (!gstatus.ok()) {
      return FromGrpcStatus(gstatus);
    }
    if (resp.result().exists()) {
      // Fast idempotency: if caller asks for the same name as an
      // existing collection, return OK. A full dim/metric compatibility
      // check would be more robust (Get on the collection, compare
      // vectors_config) but YAGNI — Slice 6 callers only ever create
      // one canonical collection per corpus.
      return absl::OkStatus();
    }
  }

  qdrant::CreateCollection req;
  req.set_collection_name(std::string(name));
  auto *vp = req.mutable_vectors_config()->mutable_params();
  vp->set_size(static_cast<uint64_t>(vector_dim));
  vp->set_distance(ToQdrantDistance(metric));

  qdrant::CollectionOperationResponse resp;
  grpc::ClientContext ctx;
  AddApiKeyIfSet(ctx, impl_->api_key);
  const auto gstatus = impl_->collections->Create(&ctx, req, &resp);
  if (!gstatus.ok()) {
    return FromGrpcStatus(gstatus);
  }
  if (!resp.result()) {
    return absl::InternalError(
        absl::StrCat("QdrantClient::CreateCollection: Qdrant returned "
                     "result=false for collection ",
                     name));
  }
  return absl::OkStatus();
}

// -----------------------------------------------------------------------------
// UpsertPoints
// -----------------------------------------------------------------------------

absl::Status QdrantClient::UpsertPoints(std::string_view collection,
                                        absl::Span<const Point> points) {
  if (collection.empty()) {
    return absl::InvalidArgumentError(
        "QdrantClient::UpsertPoints: collection is empty");
  }
  if (points.empty()) {
    return absl::OkStatus();
  }

  qdrant::UpsertPoints req;
  req.set_collection_name(std::string(collection));
  req.set_wait(true); // block until persisted — RAG seed needs confirmed writes
  auto *points_field = req.mutable_points();
  points_field->Reserve(static_cast<int>(points.size()));

  for (const auto &p : points) {
    if (p.id.empty()) {
      return absl::InvalidArgumentError(
          "QdrantClient::UpsertPoints: point id cannot be empty "
          "(deterministic IDs are required for idempotent re-seeding)");
    }
    if (p.vector.empty()) {
      return absl::InvalidArgumentError(absl::StrCat(
          "QdrantClient::UpsertPoints: point id=", p.id, " has empty vector"));
    }
    auto *ps = points_field->Add();
    ps->mutable_id()->set_uuid(p.id);
    auto *vec = ps->mutable_vectors()->mutable_vector();
    // Avoid the deprecated RepeatedField<float>* mutable_data() accessor
    // by adding elements one-by-one — works across protobuf versions.
    for (const float f : p.vector) {
      vec->add_data(f);
    }
    StringMapToQdrantPayload(p.payload, ps->mutable_payload());
  }

  qdrant::PointsOperationResponse resp;
  grpc::ClientContext ctx;
  AddApiKeyIfSet(ctx, impl_->api_key);
  const auto gstatus = impl_->points->Upsert(&ctx, req, &resp);
  if (!gstatus.ok()) {
    return FromGrpcStatus(gstatus);
  }
  return absl::OkStatus();
}

// -----------------------------------------------------------------------------
// Search
// -----------------------------------------------------------------------------

absl::StatusOr<std::vector<SearchResult>>
QdrantClient::Search(std::string_view collection,
                     absl::Span<const float> query_vec, int top_k) {
  if (collection.empty()) {
    return absl::InvalidArgumentError(
        "QdrantClient::Search: collection is empty");
  }
  if (query_vec.empty()) {
    return absl::InvalidArgumentError(
        "QdrantClient::Search: query_vec is empty");
  }
  if (top_k <= 0) {
    return absl::InvalidArgumentError(
        absl::StrCat("QdrantClient::Search: top_k must be > 0, got ", top_k));
  }

  qdrant::SearchPoints req;
  req.set_collection_name(std::string(collection));
  for (const float f : query_vec) {
    req.add_vector(f);
  }
  req.set_limit(static_cast<uint64_t>(top_k));
  // Enable payload return. WithPayloadSelector.enable=true is the
  // simplest form (returns all fields; we keep payloads small).
  req.mutable_with_payload()->set_enable(true);

  qdrant::SearchResponse resp;
  grpc::ClientContext ctx;
  AddApiKeyIfSet(ctx, impl_->api_key);
  const auto gstatus = impl_->points->Search(&ctx, req, &resp);
  if (!gstatus.ok()) {
    return FromGrpcStatus(gstatus);
  }

  std::vector<SearchResult> out;
  out.reserve(resp.result_size());
  for (const auto &sp : resp.result()) {
    SearchResult r;
    // Qdrant's PointId is a oneof — prefer uuid; fall back to num.
    if (sp.id().has_uuid()) {
      r.id = sp.id().uuid();
    } else if (sp.id().point_id_options_case() == qdrant::PointId::kNum) {
      r.id = std::to_string(sp.id().num());
    }
    r.score = sp.score();
    r.payload = QdrantPayloadToStringMap(sp.payload());
    out.push_back(std::move(r));
  }
  return out;
}

// -----------------------------------------------------------------------------
// ListCollections
// -----------------------------------------------------------------------------

absl::StatusOr<std::vector<std::string>> QdrantClient::ListCollections() {
  qdrant::ListCollectionsRequest req;
  qdrant::ListCollectionsResponse resp;
  grpc::ClientContext ctx;
  AddApiKeyIfSet(ctx, impl_->api_key);
  const auto gstatus = impl_->collections->List(&ctx, req, &resp);
  if (!gstatus.ok()) {
    return FromGrpcStatus(gstatus);
  }

  std::vector<std::string> out;
  out.reserve(resp.collections_size());
  for (const auto &c : resp.collections()) {
    out.push_back(c.name());
  }
  return out;
}

} // namespace aegis::vectordb
