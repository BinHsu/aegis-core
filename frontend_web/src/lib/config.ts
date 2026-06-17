// frontend_web/src/lib/config.ts
//
// Runtime configuration (ADR-15). The SPA is built ONCE and configured
// at deploy time by a `/config.json` served next to the bundle — the
// SAME immutable bundle runs on-prem (Dex) and on AWS (Cognito) by
// swapping config.json, instead of baking VITE_* into the build.
//
// Contract:
//   - loadConfig() MUST be awaited in main.tsx BEFORE the app renders.
//   - Every consumer reads through the synchronous getConfig() after.
//   - A missing/partial /config.json falls back to import.meta.env, so
//     `npm run dev` keeps working from a .env.local (or no config at
//     all) — delete public/config.json to get the env-fallback path.

export type DeployMode = "local" | "cloud";

export interface CognitoConfig {
  readonly authority: string;
  readonly clientId: string;
  readonly redirectUri: string;
  readonly logoutUri?: string;
}

export interface AppConfig {
  readonly deployMode: DeployMode;
  /** Resolved gateway base URL (same-host:8080 default already applied). */
  readonly gatewayEndpoint: string;
  /** Cognito settings in Cloud mode; null in Local mode. */
  readonly cognito: CognitoConfig | null;
}

interface RawConfig {
  deployMode?: string;
  gatewayEndpoint?: string | null;
  cognito?: Partial<CognitoConfig> | null;
}

let cached: AppConfig | null = null;

/**
 * Same-host:8080 (NOT hard-coded localhost) so the LAN-viewer QR flow
 * works: a phone loading the page from http://192.168.x.y:5173 computes
 * http://192.168.x.y:8080 for the gateway. Matches the old per-page
 * default that this module now centralizes.
 */
function defaultGatewayEndpoint(): string {
  if (typeof window === "undefined") return "http://localhost:8080";
  return `${window.location.protocol}//${window.location.hostname}:8080`;
}

function envStr(key: string): string | undefined {
  const v = (import.meta.env as Record<string, unknown>)[key];
  return typeof v === "string" && v !== "" ? v : undefined;
}

function normalize(raw: RawConfig): AppConfig {
  const deployMode: DeployMode =
    (raw.deployMode ?? envStr("VITE_AEGIS_DEPLOY_MODE") ?? "local") === "cloud"
      ? "cloud"
      : "local";

  const gatewayEndpoint =
    raw.gatewayEndpoint ??
    undefined ??
    envStr("VITE_AEGIS_GATEWAY_ENDPOINT") ??
    defaultGatewayEndpoint();

  let cognito: CognitoConfig | null = null;
  if (deployMode === "cloud") {
    const c = raw.cognito ?? {};
    const authority = c.authority ?? envStr("VITE_AEGIS_COGNITO_AUTHORITY");
    const clientId = c.clientId ?? envStr("VITE_AEGIS_COGNITO_CLIENT_ID");
    const redirectUri =
      c.redirectUri ?? envStr("VITE_AEGIS_COGNITO_REDIRECT_URI");
    const logoutUri = c.logoutUri ?? envStr("VITE_AEGIS_COGNITO_LOGOUT_URI");
    if (!authority || !clientId || !redirectUri) {
      throw new Error(
        "config: deployMode=cloud requires cognito.authority, cognito.clientId, " +
          "and cognito.redirectUri (from /config.json or VITE_AEGIS_COGNITO_* env).",
      );
    }
    cognito = {
      authority,
      clientId,
      redirectUri,
      ...(logoutUri ? { logoutUri } : {}),
    };
  }

  return { deployMode, gatewayEndpoint, cognito };
}

/**
 * Fetch /config.json at boot, normalize it (with env fallback), cache.
 * Call once in main.tsx and await before render.
 */
export async function loadConfig(): Promise<AppConfig> {
  let raw: RawConfig = {};
  try {
    const res = await fetch("/config.json", { cache: "no-store" });
    if (res.ok) {
      raw = (await res.json()) as RawConfig;
    }
  } catch {
    // No served config.json (e.g. `npm run dev`) — fall through to the
    // import.meta.env fallback inside normalize().
  }
  cached = normalize(raw);
  return cached;
}

/**
 * Synchronous accessor. Throws if called before loadConfig() resolves —
 * a loud failure beats a silent half-configured app.
 */
export function getConfig(): AppConfig {
  if (cached === null) {
    throw new Error(
      "config: getConfig() called before loadConfig(). loadConfig() must be " +
        "awaited in main.tsx before the app renders.",
    );
  }
  return cached;
}
