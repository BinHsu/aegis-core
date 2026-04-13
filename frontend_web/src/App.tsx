// frontend_web/src/App.tsx
//
// Shell layout. Delegates all routing to react-router; concrete pages
// live under src/pages/ and mount via the <Outlet /> below.

import { Link, Outlet, useLocation } from "react-router-dom";

export function App(): JSX.Element {
  const location = useLocation();
  const isLanding = location.pathname === "/";

  return (
    <div style={{ fontFamily: "system-ui, sans-serif", padding: "1rem" }}>
      <header
        style={{
          borderBottom: "1px solid #eee",
          paddingBottom: "0.5rem",
          marginBottom: "1rem",
        }}
      >
        <strong>🛡️ Aegis Core</strong>
        <span style={{ color: "#888", marginLeft: "0.5rem" }}>
          — real-time meeting intelligence
        </span>
      </header>

      {isLanding ? (
        <main>
          <p>
            Pick a role to continue. The same bundle serves both the{" "}
            <strong>staff host</strong> (who drives the meeting) and the{" "}
            <strong>viewer</strong> (boss / observers who see the live
            prompter).
          </p>
          <ul>
            <li>
              <Link to="/host">Open Host UI</Link> — staff-side; captures audio
              via <code>getUserMedia</code> + <code>getDisplayMedia</code>.
            </li>
            <li>
              <em>Viewer UI</em> is joined via invite link of the form{" "}
              <code>/view/&lt;session_id&gt;?token=&lt;jwt&gt;</code> (ADR-0001
              Option B).
            </li>
          </ul>
          <p style={{ color: "#888", fontSize: "0.85rem", marginTop: "2rem" }}>
            Phase 1 C1 scaffold — provider implementations are stubs; no backend
            connection yet. See <code>ROADMAP.md</code>.
          </p>
        </main>
      ) : (
        <Outlet />
      )}
    </div>
  );
}
