import { Link, useParams } from 'react-router-dom';
import StatusBadge, { normalizeHealth } from '../components/StatusBadge';
import { useClusterDetail } from '../hooks/useClusterDetail';
import type { BackupSummary, ClusterHealth, InstanceMetricsSummary, ScheduledBackupSummary, WebSocketStatus } from '../types/api';

const HEALTH_THRESHOLDS = {
  replicationLagWarningSeconds: 10,
  replicationLagCriticalSeconds: 30,
  connectionRatioWarning: 0.8,
  connectionRatioCritical: 0.9,
  diskUsageRatioWarning: 0.8,
  diskUsageRatioCritical: 0.9,
};

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

function formatBytes(value: number) {
  if (!Number.isFinite(value) || value <= 0) {
    return '0 B';
  }

  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  let nextValue = value;
  let unitIndex = 0;

  while (nextValue >= 1024 && unitIndex < units.length - 1) {
    nextValue /= 1024;
    unitIndex += 1;
  }

  return `${nextValue >= 10 || unitIndex === 0 ? nextValue.toFixed(0) : nextValue.toFixed(1)} ${units[unitIndex]}`;
}

function formatPercent(ratio: number | null) {
  if (ratio === null || !Number.isFinite(ratio)) {
    return '—';
  }

  return `${Math.round(ratio * 100)}%`;
}

function clampRatio(ratio: number | null) {
  if (ratio === null || !Number.isFinite(ratio)) {
    return null;
  }

  return Math.max(0, Math.min(ratio, 1));
}

function formatPodStatus(status?: string) {
  if (!status) {
    return 'Not reported';
  }

  return status
    .split(/[-_\s]+/)
    .filter(Boolean)
    .map((fragment) => fragment.charAt(0).toUpperCase() + fragment.slice(1))
    .join(' ');
}

function getPodStatusTone(status?: string): ClusterHealth {
  switch (status) {
    case 'healthy':
      return 'healthy';
    case 'failed':
    case 'terminating':
      return 'critical';
    case 'pending':
    case 'starting':
      return 'warning';
    default:
      return status ? 'unknown' : 'unknown';
  }
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
      return 'This cluster detail view will refetch when a matching store.changed event arrives.';
    case 'reconnecting': {
      const retryInSeconds = reconnectDelayMs ? Math.max(reconnectDelayMs / 1000, 1).toFixed(1) : 'soon';
      return `Retry ${reconnectAttempt} will start in ${retryInSeconds}s.`;
    }
    case 'error':
      return 'The last live update attempt failed. Detail refetches will resume automatically after reconnect.';
    case 'disconnected':
      return 'Live updates are paused because the socket is disconnected.';
    default:
      return 'Connecting to the live updates stream for this cluster.';
  }
}

function connectionRatio(instance: InstanceMetricsSummary) {
  if (instance.connections.maxConnections <= 0) {
    return null;
  }

  return instance.connections.total / instance.connections.maxConnections;
}

function diskUsageRatio(instance: InstanceMetricsSummary) {
  if (instance.disk.pvcCapacityBytes <= 0) {
    return null;
  }

  return instance.disk.databaseSizeBytes / instance.disk.pvcCapacityBytes;
}

function replicationLagRatio(instance: InstanceMetricsSummary) {
  if (!Number.isFinite(instance.replication.replicationLagSeconds) || instance.replication.replicationLagSeconds < 0) {
    return null;
  }

  return instance.replication.replicationLagSeconds / HEALTH_THRESHOLDS.replicationLagCriticalSeconds;
}

function toneForRatio(ratio: number | null, warning: number, critical: number): ClusterHealth {
  if (ratio === null || !Number.isFinite(ratio)) {
    return 'unknown';
  }

  if (ratio > critical) {
    return 'critical';
  }
  if (ratio > warning) {
    return 'warning';
  }
  return 'healthy';
}

function toneForLag(seconds: number): ClusterHealth {
  if (!Number.isFinite(seconds) || seconds < 0) {
    return 'unknown';
  }

  if (seconds > HEALTH_THRESHOLDS.replicationLagCriticalSeconds) {
    return 'critical';
  }
  if (seconds > HEALTH_THRESHOLDS.replicationLagWarningSeconds) {
    return 'warning';
  }
  return 'healthy';
}

function toneClass(tone: ClusterHealth) {
  return `metric-panel metric-panel--${tone}`;
}

function MeterBar({ ratio, tone, label }: { ratio: number | null; tone: ClusterHealth; label: string }) {
  const clampedRatio = clampRatio(ratio);

  return (
    <div className="metric-meter" aria-hidden="true">
      <div className="metric-meter__label-row">
        <span>{label}</span>
        <span className="metric-meter__value">{formatPercent(clampedRatio)}</span>
      </div>
      <div className="metric-meter__track">
        <span
          className={`metric-meter__fill metric-meter__fill--${tone}`}
          style={{ width: `${Math.round((clampedRatio ?? 0) * 100)}%` }}
        />
      </div>
    </div>
  );
}

function getBackupHealth(backups: BackupSummary[], scheduledBackups: ScheduledBackupSummary[]) {
  if (backups.length === 0 && scheduledBackups.length === 0) {
    return 'No backup automation recorded yet.';
  }

  const failedBackups = backups.filter((backup) => backup.error || backup.phase === 'failed');
  const suspendedSchedules = scheduledBackups.filter((backup) => backup.suspended);

  if (failedBackups.length > 0) {
    return `${failedBackups.length} backup job${failedBackups.length === 1 ? '' : 's'} reported an error.`;
  }
  if (suspendedSchedules.length > 0) {
    return `${suspendedSchedules.length} scheduled backup${suspendedSchedules.length === 1 ? ' is' : 's are'} suspended.`;
  }

  return `${backups.length} backup job${backups.length === 1 ? '' : 's'} and ${scheduledBackups.length} schedule${scheduledBackups.length === 1 ? '' : 's'} tracked.`;
}

function InstanceCard({ instance }: { instance: InstanceMetricsSummary }) {
  const normalizedHealth = normalizeHealth(instance.health);
  const podStatusTone = getPodStatusTone(instance.podStatus);
  const connectionTone = toneForRatio(
    connectionRatio(instance),
    HEALTH_THRESHOLDS.connectionRatioWarning,
    HEALTH_THRESHOLDS.connectionRatioCritical,
  );
  const replicationTone = toneForLag(instance.replication.replicationLagSeconds);
  const diskTone = toneForRatio(
    diskUsageRatio(instance),
    HEALTH_THRESHOLDS.diskUsageRatioWarning,
    HEALTH_THRESHOLDS.diskUsageRatioCritical,
  );
  const connectionSaturation = connectionRatio(instance);
  const diskPressure = diskUsageRatio(instance);
  const replicationPressure = replicationLagRatio(instance);

  return (
    <article
      className={`instance-card instance-card--${normalizedHealth}`}
      aria-label={`Instance ${instance.podName}`}
      data-health={normalizedHealth}
    >
      <div className="instance-card__header">
        <div>
          <p className="cluster-card__eyebrow">{instance.podName}</p>
          <h3 className="instance-card__title">Per-instance health snapshot</h3>
        </div>
        <StatusBadge status={instance.health} />
      </div>

      <div className="instance-card__chips">
        <span className={`detail-chip detail-chip--${podStatusTone}`}>Pod {formatPodStatus(instance.podStatus)}</span>
        <span className="detail-chip detail-chip--neutral">
          {instance.replication.isReplica ? 'Replica' : 'Primary'}
        </span>
        <span className="detail-chip detail-chip--neutral">
          Scraped {instance.scrapedAt ? formatTimestamp(instance.scrapedAt) : 'not yet'}
        </span>
      </div>

      <div className="instance-metrics">
        <section
          className={toneClass(connectionTone)}
          data-tone={connectionTone}
          aria-label={`${instance.podName} connection saturation`}
        >
          <span className="metric-panel__eyebrow">Connection saturation</span>
          <strong className="metric-panel__value">{formatPercent(connectionSaturation)}</strong>
          <p className="metric-panel__caption">
            {instance.connections.total} total / {instance.connections.maxConnections || '—'} max
          </p>
          <MeterBar ratio={connectionSaturation} tone={connectionTone} label="Capacity used" />
          <p className="metric-panel__threshold">Warning at 80%, critical above 90% of max connections.</p>
          <dl className="metric-panel__meta">
            <div>
              <dt>Active</dt>
              <dd>{instance.connections.active}</dd>
            </div>
            <div>
              <dt>Idle</dt>
              <dd>{instance.connections.idle}</dd>
            </div>
            <div>
              <dt>Idle in tx</dt>
              <dd>{instance.connections.idleInTransaction}</dd>
            </div>
          </dl>
        </section>

        <section
          className={toneClass(replicationTone)}
          data-tone={replicationTone}
          aria-label={`${instance.podName} replication lag`}
        >
          <span className="metric-panel__eyebrow">Replication lag</span>
          <strong className="metric-panel__value">{instance.replication.replicationLagSeconds.toFixed(1)}s</strong>
          <p className="metric-panel__caption">
            {instance.replication.isReplica
              ? `${instance.replication.streamingReplicas} upstream stream${instance.replication.streamingReplicas === 1 ? '' : 's'} visible`
              : 'Primary instance — replica lag should stay close to zero.'}
          </p>
          <MeterBar ratio={replicationPressure} tone={replicationTone} label="Critical threshold used" />
          <p className="metric-panel__threshold">Warning after 10s lag, critical after 30s lag.</p>
          <dl className="metric-panel__meta">
            <div>
              <dt>Replay lag</dt>
              <dd>{formatBytes(instance.replication.replayLagBytes)}</dd>
            </div>
            <div>
              <dt>WAL receiver</dt>
              <dd>{instance.replication.isWalReceiverUp ? 'Up' : 'Down'}</dd>
            </div>
          </dl>
        </section>

        <section className={toneClass(diskTone)} data-tone={diskTone} aria-label={`${instance.podName} disk usage`}>
          <span className="metric-panel__eyebrow">Disk usage</span>
          <strong className="metric-panel__value">{formatPercent(diskPressure)}</strong>
          <p className="metric-panel__caption">
            {instance.disk.pvcCapacityBytes > 0
              ? `${formatBytes(instance.disk.databaseSizeBytes)} of ${formatBytes(instance.disk.pvcCapacityBytes)} used`
              : 'PVC capacity has not been reported yet.'}
          </p>
          <MeterBar ratio={diskPressure} tone={diskTone} label="PVC consumed" />
          <p className="metric-panel__threshold">Warning at 80%, critical above 90% of reported PVC capacity.</p>
          <dl className="metric-panel__meta">
            <div>
              <dt>Database size</dt>
              <dd>{formatBytes(instance.disk.databaseSizeBytes)}</dd>
            </div>
            <div>
              <dt>PVC capacity</dt>
              <dd>{instance.disk.pvcCapacityBytes > 0 ? formatBytes(instance.disk.pvcCapacityBytes) : '—'}</dd>
            </div>
          </dl>
        </section>
      </div>

      {instance.scrapeError ? (
        <section className="diagnostic-card diagnostic-card--error" aria-label={`${instance.podName} scrape diagnostics`}>
          <h4>Scrape diagnostics</h4>
          <p>{instance.scrapeError}</p>
        </section>
      ) : (
        <section className="diagnostic-card" aria-label={`${instance.podName} scrape diagnostics`}>
          <h4>Scrape diagnostics</h4>
          <p>No scrape errors were reported for this instance.</p>
        </section>
      )}
    </article>
  );
}

export default function ClusterDetail() {
  const params = useParams();
  const namespace = params.namespace;
  const name = params.name;

  const {
    detail,
    metrics,
    cluster,
    isLoading,
    isRefreshing,
    error,
    refetch,
    lastLoadedAt,
    lastRefreshReason,
    lastEvent,
    liveUpdates,
  } = useClusterDetail(namespace, name);

  if (!namespace || !name) {
    return (
      <section className="state-card state-card--error" aria-live="assertive">
        <h2>Cluster route is incomplete</h2>
        <p>Deckhand needs both a namespace and cluster name to render this dashboard.</p>
      </section>
    );
  }

  const liveDescription = liveStatusDescription(
    liveUpdates.status,
    liveUpdates.reconnectDelayMs,
    liveUpdates.reconnectAttempt,
  );
  const backupSummary = getBackupHealth(detail.backups, detail.scheduledBackups);
  const metricsScrapedLabel = metrics.scrapedAt ? formatTimestamp(metrics.scrapedAt) : metrics.scrapeError || '—';
  const backupsPath = `/clusters/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/backups`;

  return (
    <section className="cluster-detail-page" aria-labelledby="cluster-detail-title">
      <div className="cluster-detail-page__hero">
        <div className="cluster-detail-page__intro">
          <div className="cluster-detail-page__actions">
            <Link className="secondary-button cluster-detail-page__back-link" to="/">
              Back to overview
            </Link>
            <Link className="secondary-button cluster-detail-page__back-link" to={backupsPath}>
              Manage backups
            </Link>
          </div>
          <p className="eyebrow">{cluster.namespace || namespace}</p>
          <h2 id="cluster-detail-title">{cluster.name || name}</h2>
          <p className="lede">
            Inspect pod status, connection pressure, replication lag, disk utilization, and live scrape diagnostics for this CloudNativePG cluster.
          </p>
          <div className="cluster-detail-page__headline-row">
            <StatusBadge status={metrics.overallHealth} />
            {isRefreshing ? <span className="refresh-state">Refreshing detail…</span> : null}
          </div>
        </div>

        <aside className={`live-status live-status--${liveUpdates.status}`} aria-live="polite">
          <div className="live-status__label-row">
            <span className="live-status__label">Detail live updates</span>
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
              <dt>Last matching event</dt>
              <dd>
                {lastEvent
                  ? `${lastEvent.action} ${lastEvent.namespace}/${lastEvent.name}`
                  : 'Waiting for a matching cluster change'}
              </dd>
            </div>
          </dl>
          {liveUpdates.lastError ? <p className="live-status__error">{liveUpdates.lastError}</p> : null}
        </aside>
      </div>

      {isLoading ? (
        <section className="state-card" aria-live="polite">
          <h3>Loading cluster detail</h3>
          <p>Deckhand is fetching /api/clusters/{namespace}/{name} and the matching metrics snapshot.</p>
        </section>
      ) : null}

      {error ? (
        <section className="state-card state-card--error" aria-live="assertive">
          <h3>Could not load the cluster detail</h3>
          <p>{error}</p>
          <button className="secondary-button" type="button" onClick={() => refetch('manual')}>
            Retry request
          </button>
        </section>
      ) : null}

      {!isLoading && !error ? (
        <>
          <div className="summary-grid summary-grid--detail" aria-label="Cluster detail summaries">
            <article className="summary-card">
              <span className="summary-card__label">Instances ready</span>
              <strong className="summary-card__value tabular-values">
                {cluster.readyInstances}
                <span className="cluster-metadata__separator">/</span>
                {cluster.desiredInstances}
              </strong>
            </article>
            <article className="summary-card">
              <span className="summary-card__label">Current primary</span>
              <strong className="summary-card__value summary-card__value--text">{cluster.currentPrimary || '—'}</strong>
            </article>
            <article className="summary-card">
              <span className="summary-card__label">Backups tracked</span>
              <strong className="summary-card__value tabular-values">{detail.backups.length}</strong>
            </article>
            <article className="summary-card">
              <span className="summary-card__label">Schedules tracked</span>
              <strong className="summary-card__value tabular-values">{detail.scheduledBackups.length}</strong>
            </article>
            <article className="summary-card">
              <span className="summary-card__label">Metrics scraped</span>
              <strong className="summary-card__value summary-card__value--text">{metricsScrapedLabel}</strong>
            </article>
          </div>

          <div className="detail-grid">
            <article className="detail-card">
              <div className="detail-card__header">
                <div>
                  <p className="cluster-card__eyebrow">Cluster summary</p>
                  <h3>Runtime contract</h3>
                </div>
                <StatusBadge status={metrics.overallHealth} />
              </div>
              <dl className="cluster-metadata cluster-metadata--detail">
                <div>
                  <dt>Phase</dt>
                  <dd>{cluster.phase || 'Unknown'}</dd>
                </div>
                <div>
                  <dt>Reason</dt>
                  <dd>{cluster.phaseReason || '—'}</dd>
                </div>
                <div>
                  <dt>Image</dt>
                  <dd>{cluster.image || '—'}</dd>
                </div>
                <div>
                  <dt>Created</dt>
                  <dd>{formatTimestamp(cluster.createdAt)}</dd>
                </div>
                <div>
                  <dt>First recoverability</dt>
                  <dd>{formatTimestamp(cluster.firstRecoverabilityPoint)}</dd>
                </div>
                <div>
                  <dt>Last successful backup</dt>
                  <dd>{formatTimestamp(cluster.lastSuccessfulBackup)}</dd>
                </div>
              </dl>
            </article>

            <article className="detail-card">
              <div className="detail-card__header">
                <div>
                  <p className="cluster-card__eyebrow">Diagnostics</p>
                  <h3>Scrape and backup signals</h3>
                </div>
              </div>
              <div className="diagnostic-stack">
                <section className={metrics.scrapeError ? 'diagnostic-card diagnostic-card--error' : 'diagnostic-card'}>
                  <h4>Cluster scrape status</h4>
                  <p>{metrics.scrapeError || 'All instance scrapes are currently healthy.'}</p>
                </section>
                <section className="diagnostic-card">
                  <h4>Backup posture</h4>
                  <p>{backupSummary}</p>
                </section>
              </div>
            </article>
          </div>

          {metrics.instances.length === 0 ? (
            <section className="state-card" aria-live="polite">
              <h3>No instance metrics yet</h3>
              <p>The metrics endpoint has not published a per-instance snapshot for this cluster.</p>
            </section>
          ) : (
            <section className="instance-section" aria-labelledby="instance-section-title">
              <div className="instance-section__header">
                <div>
                  <p className="cluster-card__eyebrow">Per-instance metrics</p>
                  <h3 id="instance-section-title">Pods, thresholds, and scrape diagnostics</h3>
                </div>
                <p className="instance-section__caption">
                  Warning and critical treatments mirror the backend thresholds for replication lag, connection saturation, and disk pressure.
                </p>
              </div>
              <div className="instance-grid" aria-live="polite">
                {metrics.instances.map((instance) => (
                  <InstanceCard key={instance.podName} instance={instance} />
                ))}
              </div>
            </section>
          )}
        </>
      ) : null}
    </section>
  );
}
