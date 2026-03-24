import { NavLink, Route, Routes, useMatch } from 'react-router-dom';
import Backups from './pages/Backups';
import ClusterDetail from './pages/ClusterDetail';
import Overview from './pages/Overview';
import Restore from './pages/Restore';

function RoutePlaceholder() {
  return (
    <section className="route-placeholder" aria-labelledby="route-placeholder-title">
      <h2 id="route-placeholder-title">Route not ready yet</h2>
      <p>Additional routed views will land in later milestones.</p>
    </section>
  );
}

function AppShell() {
  const restoreMatch = useMatch('/clusters/:namespace/:name/restore');
  const backupsMatch = useMatch('/clusters/:namespace/:name/backups');
  const detailMatch = useMatch('/clusters/:namespace/:name');
  const routeContext = restoreMatch ?? backupsMatch ?? detailMatch;

  const detailPath = routeContext
    ? `/clusters/${routeContext.params.namespace ?? ''}/${routeContext.params.name ?? ''}`
    : null;
  const backupsPath = routeContext ? `${detailPath}/backups` : null;
  const restorePath = routeContext ? `${detailPath}/restore` : null;
  const detailLabel = routeContext
    ? `${routeContext.params.namespace ?? 'unknown'}/${routeContext.params.name ?? 'unknown'}`
    : null;

  return (
    <>
      <header className="app-shell__header">
        <div className="app-shell__container app-shell__header-inner">
          <div>
            <p className="eyebrow">CloudNativePG operations dashboard</p>
            <h1 className="app-shell__title">Deckhand overview</h1>
            <p className="app-shell__subtitle">
              Live cluster health, backup freshness, and namespace-scoped filtering served directly from the Deckhand binary.
            </p>
          </div>

          <nav aria-label="Primary" className="app-shell__nav">
            <NavLink
              className={({ isActive }) =>
                isActive ? 'app-shell__nav-link app-shell__nav-link--active' : 'app-shell__nav-link'
              }
              to="/"
              end
            >
              Overview
            </NavLink>
            {detailPath && detailLabel ? (
              <NavLink
                className={({ isActive }) =>
                  isActive ? 'app-shell__nav-link app-shell__nav-link--active' : 'app-shell__nav-link'
                }
                to={detailPath}
                end
              >
                {detailLabel}
              </NavLink>
            ) : null}
            {backupsPath ? (
              <NavLink
                className={({ isActive }) =>
                  isActive ? 'app-shell__nav-link app-shell__nav-link--active' : 'app-shell__nav-link'
                }
                to={backupsPath}
                end
              >
                Backups
              </NavLink>
            ) : null}
            {restorePath ? (
              <NavLink
                className={({ isActive }) =>
                  isActive ? 'app-shell__nav-link app-shell__nav-link--active' : 'app-shell__nav-link'
                }
                to={restorePath}
                end
              >
                Restore
              </NavLink>
            ) : null}
          </nav>
        </div>
      </header>

      <main className="app-shell__main">
        <div className="app-shell__container">
          <Routes>
            <Route path="/" element={<Overview />} />
            <Route path="/clusters/:namespace/:name/backups" element={<Backups />} />
            <Route path="/clusters/:namespace/:name/restore" element={<Restore />} />
            <Route path="/clusters/:namespace/:name" element={<ClusterDetail />} />
            <Route path="*" element={<RoutePlaceholder />} />
          </Routes>
        </div>
      </main>
    </>
  );
}

export default function App() {
  return (
    <div className="app-shell">
      <AppShell />
    </div>
  );
}
