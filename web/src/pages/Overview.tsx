import { useMemo, useState, type ChangeEvent } from 'react';
import { Link } from 'react-router-dom';
import StatusBadge, { normalizeHealth } from '../components/StatusBadge';
import { useClusters } from '../hooks/useClusters';
import type { ClusterHealth, ClusterOverviewSummary, WebSocketStatus } from '../types/api';

const HEALTH_FILTER_OPTIONS: Array<{ value: 'all' | ClusterHealth; label: string }> = [
  { value: 'all', label: 'All health states' },
  { value: 'healthy', label: 'Healthy' },
  { value: 'warning', label: 'Warning' },
  { value: 'critical', label: 'Critical' },
  { value: 'unknown', label: 'Unknown' },
];

function formatTimestamp(value: number | string | null | undefined) {
  if (!value) {
    return '—';
  }

  const date = typeof value === 'number' ? new Date(value) : new Date(value);
  if (Number.isNaN(date.valueOf())) {
    return '—';
  }

  return new Intl.DateTimeFormat(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(date);
}

function formatRelativeAge(value: string | null | undefined) {
  if (!value) {
    return 'No successful backup recorded';
  }

  const timestamp = new Date(value);
  if (Number.isNaN(timestamp.valueOf())) {
    return 'Unknown backup time';
  }

  const diffMs = Date.now() - timestamp.valueOf();
  const minuteMs = 60 * 1000;
  const hourMs = 60 * minuteMs;
  const dayMs = 24 * hourMs;

  if (diffMs < minuteMs) {
    return 'Just now';
  }
  if (diffMs < hourMs) {
    return `${Math.floor(diffMs / minuteMs)}m ago`;
  }
  if (diffMs < dayMs) {
    return `${Math.floor(diffMs / hourMs)}h ago`;
  }
  return `${Math.floor(diffMs / dayMs)}d ago`;
}

function getBackupFreshnessTone(value: string | null | undefined) {
  if (!value) {
    return 'missing';
  }

  const timestamp = new Date(value);
  if (Number.isNaN(timestamp.valueOf())) {
    return 'missing';
  }

  const diffMs = Date.now() - timestamp.valueOf();
  const hourMs = 60 * 60 * 1000;

  if (diffMs <= 6 * hourMs) {
    return 'fresh';
  }
  if (diffMs <= 24 * hourMs) {
    return 'recent';
  }
  return 'stale';
}

function liveStatusLabel(status: WebSocketStatus) {
  switch (status) {
    case 'connected':
      return 'Connected';
    case 'reconnecting':
      return 'Reconnecting';
    case 'error':
      return 'Degraded';
    case 'disconnected':
      return 'Disconnected';
    default:
      return 'Connecting';
  }
}

function liveStatusDescription(status: WebSocketStatus, reconnectDelayMs: number | null, reconnectAttempt: number) {
  switch (status) {
    case 'connected':
      return 'Live overview updates are flowing from /ws.';
    case 'reconnecting': {
      const nextAttemptInSeconds = reconnectDelayMs ? Math.max(reconnectDelayMs / 1000, 1).toFixed(1) : 'soon';
      return `Retry ${reconnectAttempt} will start in ${nextAttemptInSeconds}s.`;
    }
    case 'error':
      return 'The last /ws attempt failed. Deckhand will keep retrying automatically.';
    case 'disconnected':
      return 'Live updates are paused because the socket is disconnected.';
    default:
      return 'Connecting to the live updates stream.';
  }
}

function getHealthCounts(clusters: ClusterOverviewSummary[]) {
  return clusters.reduce(
    (accumulator, cluster) => {
      const health = normalizeHealth(cluster.overallHealth);
      accumulator[health] += 1;
      return accumulator;
    },
    {
      healthy: 0,
      warning: 0,
      critical: 0,
      unknown: 0,
    },
  );
}

function clusterDetailPath(cluster: ClusterOverviewSummary) {
  return `/clusters/${encodeURIComponent(cluster.namespace)}/${encodeURIComponent(cluster.name)}`;
}

function ClusterCard({ cluster }: { cluster: ClusterOverviewSummary }) {
  const backupTone = getBackupFreshnessTone(cluster.lastSuccessfulBackup);
  const clusterLabel = `${cluster.namespace}/${cluster.name}`;

  return (
    <article className="cluster-card cluster-card--interactive" aria-label={clusterLabel}>
      <Link
        className="cluster-card__link"
        to={clusterDetailPath(cluster)}
        aria-label={`Open detail dashboard for ${clusterLabel}`}
      >
        <div className="cluster-card__header">
          <div>
            <p className="cluster-card__eyebrow">{cluster.namespace}</p>
            <div className="cluster-card__title-row">
              <h3 className="cluster-card__title">{cluster.name}</h3>
              <span className="cluster-card__route-chip">Open detail</span>
            </div>
          </div>
          <StatusBadge status={cluster.overallHealth} />
        </div>

        <dl className="cluster-metadata">
          <div>
            <dt>Phase</dt>
            <dd>{cluster.phase || 'Unknown'}</dd>
          </div>
          <div>
            <dt>Phase reason</dt>
            <dd>{cluster.phaseReason || '—'}</dd>
          </div>
          <div>
            <dt>Instances</dt>
            <dd>
              <span className="tabular-values">{cluster.readyInstances}</span>
              <span className="cluster-metadata__separator">/</span>
              <span className="tabular-values">{cluster.desiredInstances}</span>
              <span className="cluster-metadata__suffix"> ready</span>
            </dd>
          </div>
          <div>
            <dt>Primary</dt>
            <dd>{cluster.currentPrimary || '—'}</dd>
          </div>
          <div>
            <dt>Last backup</dt>
            <dd>
              <span className={`backup-pill backup-pill--${backupTone}`}>{formatRelativeAge(cluster.lastSuccessfulBackup)}</span>
            </dd>
          </div>
          <div>
            <dt>Metrics scraped</dt>
            <dd>{cluster.metricsScrapedAt ? formatTimestamp(cluster.metricsScrapedAt) : cluster.metricsScrapeError}</dd>
          </div>
        </dl>

        <div className="cluster-card__footer">
          <strong className="cluster-card__cta">Inspect live health details</strong>
          <span className="cluster-card__cta-meta">Pod status, threshold alerts, and scrape diagnostics</span>
        </div>
      </Link>
    </article>
  );
}

export default function Overview() {
  const {
    items,
    namespaces,
    selectedNamespace,
    setSelectedNamespace,
    isLoading,
    isRefreshing,
    error,
    refetch,
    lastLoadedAt,
    lastRefreshReason,
    lastEvent,
    liveUpdates,
  } = useClusters();

  const [selectedHealth, setSelectedHealth] = useState<'all' | ClusterHealth>('all');

  return (
    <OverviewContent
      items={items}
      namespaces={namespaces}
      selectedNamespace={selectedNamespace}
      setSelectedNamespace={setSelectedNamespace}
      isLoading={isLoading}
      isRefreshing={isRefreshing}
      error={error}
      refetch={refetch}
      lastLoadedAt={lastLoadedAt}
      lastRefreshReason={lastRefreshReason}
      lastEvent={lastEvent}
      liveUpdates={liveUpdates}
      selectedHealth={selectedHealth}
      setSelectedHealth={setSelectedHealth}
    />
  );
}

interface OverviewContentProps {
  items: ClusterOverviewSummary[];
  namespaces: { name: string; clusterCount: number }[];
  selectedNamespace: string;
  setSelectedNamespace: (namespace: string) => void;
  isLoading: boolean;
  isRefreshing: boolean;
  error: string | null;
  refetch: (reason?: 'initial-load' | 'filter-change' | 'live-update' | 'manual') => void;
  lastLoadedAt: number | null;
  lastRefreshReason: string;
  lastEvent: { namespace: string; name: string; action: string; occurredAt: string } | null;
  liveUpdates: {
    status: WebSocketStatus;
    reconnectAttempt: number;
    reconnectDelayMs: number | null;
    lastMessageAt: number | null;
    lastError: string | null;
  };
  selectedHealth: 'all' | ClusterHealth;
  setSelectedHealth: (nextValue: 'all' | ClusterHealth) => void;
}

function OverviewContent({
  items,
  namespaces,
  selectedNamespace,
  setSelectedNamespace,
  isLoading,
  isRefreshing,
  error,
  refetch,
  lastLoadedAt,
  lastRefreshReason,
  lastEvent,
  liveUpdates,
  selectedHealth,
  setSelectedHealth,
}: OverviewContentProps) {
  const filteredItems = useMemo(() => {
    if (selectedHealth === 'all') {
      return items;
    }
    return items.filter((cluster) => normalizeHealth(cluster.overallHealth) === selectedHealth);
  }, [items, selectedHealth]);

  const healthCounts = useMemo(() => getHealthCounts(items), [items]);

  const onNamespaceChange = (event: ChangeEvent<HTMLSelectElement>) => {
    setSelectedNamespace(event.target.value);
  };

  const onHealthChange = (event: ChangeEvent<HTMLSelectElement>) => {
    setSelectedHealth(event.target.value as 'all' | ClusterHealth);
  };

  const liveDescription = liveStatusDescription(
    liveUpdates.status,
    liveUpdates.reconnectDelayMs,
    liveUpdates.reconnectAttempt,
  );

  const showInitialEmptyState = !isLoading && !error && items.length === 0;
  const showFilteredEmptyState = !isLoading && !error && items.length > 0 && filteredItems.length === 0;

  return (
    <section className="overview-page" aria-labelledby="overview-title">
      <div className="overview-page__hero">
        <div>
          <p className="eyebrow">CloudNativePG overview</p>
          <h2 id="overview-title">Cluster overview</h2>
          <p className="lede">
            Every discovered cluster, its live health, and the latest backup freshness in one embedded dashboard.
          </p>
        </div>

        <aside className={`live-status live-status--${liveUpdates.status}`} aria-live="polite">
          <div className="live-status__label-row">
            <span className="live-status__label">Live updates</span>
            <span className={`live-status__pill live-status__pill--${liveUpdates.status}`}>
              {liveStatusLabel(liveUpdates.status)}
            </span>
          </div>
          <p className="live-status__description">{liveDescription}</p>
          <dl className="live-status__meta">
            <div>
              <dt>Last refresh</dt>
              <dd>{formatTimestamp(lastLoadedAt)}</dd>
            </div>
            <div>
              <dt>Refresh source</dt>
              <dd>{lastRefreshReason.replace('-', ' ')}</dd>
            </div>
            <div>
              <dt>Last socket event</dt>
              <dd>
                {lastEvent
                  ? `${lastEvent.action} ${lastEvent.namespace}/${lastEvent.name}`
                  : 'Waiting for the first change event'}
              </dd>
            </div>
          </dl>
          {liveUpdates.lastError ? <p className="live-status__error">{liveUpdates.lastError}</p> : null}
        </aside>
      </div>

      <div className="summary-grid" aria-label="Overview summaries">
        <article className="summary-card">
          <span className="summary-card__label">Clusters</span>
          <strong className="summary-card__value tabular-values">{items.length}</strong>
        </article>
        <article className="summary-card">
          <span className="summary-card__label">Namespaces</span>
          <strong className="summary-card__value tabular-values">{namespaces.length}</strong>
        </article>
        <article className="summary-card summary-card--healthy">
          <span className="summary-card__label">Healthy</span>
          <strong className="summary-card__value tabular-values">{healthCounts.healthy}</strong>
        </article>
        <article className="summary-card summary-card--warning">
          <span className="summary-card__label">Warning</span>
          <strong className="summary-card__value tabular-values">{healthCounts.warning}</strong>
        </article>
        <article className="summary-card summary-card--critical">
          <span className="summary-card__label">Critical</span>
          <strong className="summary-card__value tabular-values">{healthCounts.critical}</strong>
        </article>
      </div>

      <section className="control-panel" aria-label="Overview filters">
        <div className="filters-grid">
          <label className="field" htmlFor="namespace-filter">
            <span className="field__label">Namespace</span>
            <select id="namespace-filter" value={selectedNamespace} onChange={onNamespaceChange}>
              <option value="">All namespaces</option>
              {namespaces.map((namespace) => (
                <option key={namespace.name} value={namespace.name}>
                  {namespace.name} ({namespace.clusterCount})
                </option>
              ))}
            </select>
          </label>

          <label className="field" htmlFor="health-filter">
            <span className="field__label">Health</span>
            <select id="health-filter" value={selectedHealth} onChange={onHealthChange}>
              {HEALTH_FILTER_OPTIONS.map((option) => (
                <option key={option.value} value={option.value}>
                  {option.label}
                </option>
              ))}
            </select>
          </label>
        </div>

        <div className="control-panel__actions">
          <button className="secondary-button" type="button" onClick={() => refetch('manual')}>
            Refresh now
          </button>
          {isRefreshing ? <span className="refresh-state">Refreshing…</span> : null}
        </div>
      </section>

      {isLoading ? (
        <section className="state-card" aria-live="polite">
          <h3>Loading cluster overview</h3>
          <p>Deckhand is fetching the current cluster list from /api/clusters.</p>
        </section>
      ) : null}

      {error ? (
        <section className="state-card state-card--error" aria-live="assertive">
          <h3>Could not load the cluster overview</h3>
          <p>{error}</p>
          <button className="secondary-button" type="button" onClick={() => refetch('manual')}>
            Retry request
          </button>
        </section>
      ) : null}

      {showInitialEmptyState ? (
        <section className="state-card" aria-live="polite">
          <h3>No clusters discovered yet</h3>
          <p>The selected namespace scope returned no CloudNativePG clusters.</p>
        </section>
      ) : null}

      {showFilteredEmptyState ? (
        <section className="state-card" aria-live="polite">
          <h3>No clusters match these filters</h3>
          <p>Try clearing the namespace or health filter to broaden the overview.</p>
        </section>
      ) : null}

      {!isLoading && !error && filteredItems.length > 0 ? (
        <div className="cluster-grid" aria-live="polite">
          {filteredItems.map((cluster) => (
            <ClusterCard key={`${cluster.namespace}/${cluster.name}`} cluster={cluster} />
          ))}
        </div>
      ) : null}
    </section>
  );
}
