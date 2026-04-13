// frontend_web/src/pages/Viewer/ViewerPage.tsx
//
// Phase 1 C1 placeholder. Phase 1 C4 wires the
// TranscriptStreamProvider and renders the rolling 5-line prompter.

import { useParams, useSearchParams } from "react-router-dom";

export function ViewerPage(): JSX.Element {
  const { sessionId } = useParams<{ sessionId: string }>();
  const [searchParams] = useSearchParams();
  const token = searchParams.get("token");

  return (
    <main>
      <h2>Viewer</h2>
      <p>
        Session: <code>{sessionId}</code>
      </p>
      <p>
        Token present: <code>{token ? "yes" : "no"}</code>
      </p>
      <p style={{ color: "#888" }}>
        Phase 1 C4 will wire up the <code>TranscriptStreamProvider</code> and
        render the rolling 5-line prompter here.
      </p>
    </main>
  );
}
