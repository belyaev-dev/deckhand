import { Link, useParams } from 'react-router-dom';
import { useBackups } from '../hooks/useBackups';
import type { BackupSummary, ClusterHealth, ScheduledBackupSummary, WebSocketStatus } from '../types/api';

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

function getRelativeAgeParts(value: string | null | undefined) {
  if (!value) {
    return { value: 'No successful backup yet', tone: 'missing' as const };
  }

  const timestamp = new Date(value);
  if (Number.isNaN(timestamp.valueOf())) {
    return { value: 'Unknown backup time', tone: 'missing' as const };
  }

  const diffMs = Date.now() - timestamp.valueOf();
  const minuteMs = 60 * 1000;
  const hourMs = 60 * minuteMs;
  const dayMs = 24 * hourMs;

  if (diffMs < minuteMs) {
    return { value: 'Just now', tone: 'fresh' as const };
  }
  if (diffMs < hourMs) {
    return { value: `${Math.floor(diffMs / minuteMs)}m ago`, tone: 'fresh' as const };
  }
  if (diffMs < 6 * hourMs) {
    return { value: `${Math.floor(diffMs / hourMs)}h ago`, tone: 'fresh' as const };
  }
  if (diffMs < dayMs) {
    return { value: `${Math.floor(diffMs / hourMs)}h ago`, tone: 'recent' as const };
  }
  return { value: `${Math.floor(diffMs / dayMs)}d ago`, tone: 'stale' as const };
}

function backupHealthLabel(tone: 'fresh' | 'recent' | 'stale' | 'missing') {
  switch (tone) {
    case 'fresh':
      return 'Fresh';
    case 'recent':
      return 'Recent';
    case 'stale':
      return 'Stale';
    default:
      return 'Missing';
  }
}

function summaryToneForFreshness(tone: 'fresh' | 'recent' | 'stale' | 'missing'): ClusterHealth {
  switch (tone) {
    case 'fresh':
      return 'healthy';
    case 'recent':
      return 'warning';
    case 'stale':
    case 'missing':
      return 'critical';
    default:
      return 'unknown';
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
      return 'This page refetches GET /api/clusters/:namespace/:name/backups when matching store.changed events arrive.';
    case 'reconnecting': {
      const retryInSeconds = reconnectDelayMs ? Math.max(reconnectDelayMs / 1000, 1).toFixed(1) : 'soon';
      return `Retry ${reconnectAttempt} will start in ${retryInSeconds}s.`;
    }
    case 'error':
      return 'The last live update attempt failed. Backup history will refresh again after reconnect.';
    case 'disconnected':
      return 'Live updates are paused because the socket is disconnected.';
    default:
      return 'Connecting to the live updates stream for backup progress changes.';
  }
}

function phaseTone(phase?: string): ClusterHealth {
  switch ((phase ?? '').toLowerCase()) {
    case 'completed':
    case 'succeeded':
      return 'healthy';
    case 'running':
    case 'pending':
    case 'starting':
      return 'warning';
    case 'failed':
    case 'error':
      return 'critical';
    default:
      return 'unknown';
  }
}

function formatPhaseLabel(phase?: string) {
  if (!phase) {
    return 'Unknown';
  }

  return phase
    .split(/[-_\s]+/)
    .filter(Boolean)
    .map((fragment) => fragment.charAt(0).toUpperCase() + fragment.slice(1))
    .join(' ');
}

function timestampValue(value?: string | null) {
  if (!value) {
    return 0;
  }
  const parsed = new Date(value).valueOf();
  return Number.isNaN(parsed) ? 0 : parsed;
}

function formatDuration(startedAt?: string | null, stoppedAt?: string | null) {
  const started = timestampValue(startedAt);
  const stopped = timestampValue(stoppedAt);

  if (!started && !stopped) {
    return '—';
  }
  if (started && !stopped) {
    return 'In progress';
  }
  if (!started && stopped) {
    return 'Completed';
  }

  const diffMs = Math.max(stopped - started, 0);
  const seconds = Math.floor(diffMs / 1000);
  const minutes = Math.floor(seconds / 60);
  const hours = Math.floor(minutes / 60);

  if (hours > 0) {
    return `${hours}h ${minutes % 60}m`;
  }
  if (minutes > 0) {
    return `${minutes}m ${seconds % 60}s`;
  }
  return `${seconds}s`;
}

function scheduleStatusLabel(schedule: ScheduledBackupSummary) {
  if (schedule.suspended) {
    return 'Suspended';
  }
  if (schedule.immediate) {
    return 'Immediate';
  }
  return 'Active';
}

function scheduleStatusTone(schedule: ScheduledBackupSummary): ClusterHealth {
  if (schedule.suspended) {
    return 'critical';
  }
  if (schedule.immediate) {
    return 'warning';
  }
  return 'healthy';
}

function sortedBackups(backups: BackupSummary[]) {
  return [...backups].sort((left, right) => {
    const timeDiff = timestampValue(right.startedAt ?? right.createdAt) - timestampValue(left.startedAt ?? left.createdAt);
    if (timeDiff !== 0) {
      return timeDiff;
    }
    return left.name.localeCompare(right.name);
  });
}

function sortedSchedules(scheduledBackups: ScheduledBackupSummary[]) {
  return [...scheduledBackups].sort((left, right) => {
    const timeDiff = timestampValue(left.nextScheduleTime) - timestampValue(right.nextScheduleTime);
    if (timeDiff !== 0) {
      return timeDiff;
    }
    return left.name.localeCompare(right.name);
  });
}

export default function Backups() {
  const params = useParams();
  const namespace = params.namespace;
  const name = params.name;

  const {
    data,
    cluster,
    isLoading,
    isRefreshing,
    isSubmitting,
    error,
    submitError,
    triggerBackup,
    refetch,
    lastLoadedAt,
    lastRefreshReason,
    lastEvent,
    liveUpdates,
  } = useBackups(namespace, name);

  if (!namespace || !name) {
    return (
      <section className="state-card state-card--error" aria-live="assertive">
        <h2>Backup route is incomplete</h2>
        <p>Deckhand needs both a namespace and cluster name to manage cluster backups.</p>
      </section>
    );
  }

  const backupsPath = `/clusters/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/backups`;
  const clusterPath = `/clusters/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
  const restorePath = `/clusters/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/restore`;
  const freshness = getRelativeAgeParts(cluster.lastSuccessfulBackup);
  const freshnessLabel = backupHealthLabel(freshness.tone);
  const freshnessSummaryTone = summaryToneForFreshness(freshness.tone);
  const orderedBackups = sortedBackups(data.backups);
  const orderedSchedules = sortedSchedules(data.scheduledBackups);
  const hasRenderableData = Boolean(cluster.name || data.backups.length > 0 || data.scheduledBackups.length > 0);

  const handleTriggerBackup = async () => {
    try {
      await triggerBackup();
    } catch {
      // The hook already exposes the surfaced error state for the page.
    }
  };

  const liveDescription = liveStatusDescription(
    liveUpdates.status,
    liveUpdates.reconnectDelayMs,
    liveUpdates.reconnectAttempt,
  );

  return (
    <section className="backups-page" aria-labelledby="backups-page-title">
      <div className="backups-page__hero">
        <div className="cluster-detail-page__intro">
          <div className="cluster-detail-page__actions">
            <Link className="secondary-button cluster-detail-page__back-link" to={clusterPath}>
              Back to cluster detail
            </Link>
            <Link className="secondary-button cluster-detail-page__back-link" to={restorePath}>
              Start restore flow
            </Link>
            <Link className="secondary-button cluster-detail-page__back-link" to="/">
              Overview
            </Link>
          </div>
          <p className="eyebrow">{cluster.namespace || namespace}</p>
          <h2 id="backups-page-title">Backups for {cluster.name || name}</h2>
          <p className="lede">
            Review schedule coverage, inspect backup history, and trigger an on-demand backup without leaving this routed workflow.
          </p>
          <div className="cluster-detail-page__headline-row">
            <span className={`backup-pill backup-pill--${freshness.tone}`}>{freshnessLabel}</span>
            <span className="backups-page__headline-copy">Last successful backup {freshness.value.toLowerCase()}.</span>
            {isRefreshing ? <span className="refresh-state">Refreshing backups…</span> : null}
            {isSubmitting ? <span className="refresh-state">Creating backup…</span> : null}
          </div>
        </div>

        <aside className={`live-status live-status--${liveUpdates.status}`} aria-live="polite">
          <div className="live-status__label-row">
            <span className="live-status__label">Backup live updates</span>
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
                  ? `${lastEvent.kind} ${lastEvent.action} ${lastEvent.namespace}/${lastEvent.name}`
                  : 'Waiting for a matching backup, schedule, or cluster change'}
              </dd>
            </div>
          </dl>
          {liveUpdates.lastError ? <p className="live-status__error">{liveUpdates.lastError}</p> : null}
        </aside>
      </div>

      <section className="control-panel backups-control-panel" aria-label="Backup actions">
        <div className="backups-control-panel__content">
          <span className="field__label">On-demand backup</span>
          <h3>Trigger an immediate backup for {cluster.name || name}</h3>
          <p>
            Deckhand posts a real backup-create request, keeps the existing schedule and history visible, and relies on refetches for post-create truth.
          </p>
          {submitError ? <p className="backups-control-panel__error">{submitError}</p> : null}
        </div>
        <div className="control-panel__actions">
          <button className="primary-button" type="button" onClick={handleTriggerBackup} disabled={isSubmitting || isLoading}>
            {isSubmitting ? 'Creating backup…' : 'Backup now'}
          </button>
          <button className="secondary-button" type="button" onClick={() => refetch('manual')}>
            Refresh now
          </button>
        </div>
      </section>

      {isLoading && !hasRenderableData ? (
        <section className="state-card" aria-live="polite">
          <h3>Loading backup management</h3>
          <p>Deckhand is fetching /api/clusters/{namespace}/{name}/backups.</p>
        </section>
      ) : null}

      {error ? (
        <section className="state-card state-card--error" aria-live="assertive">
          <h3>Could not load backup management</h3>
          <p>{error}</p>
          <button className="secondary-button" type="button" onClick={() => refetch('manual')}>
            Retry request
          </button>
        </section>
      ) : null}

      {hasRenderableData ? (
        <>
          <div className="summary-grid summary-grid--detail" aria-label="Backup summaries">
            <article className={`summary-card summary-card--${freshnessSummaryTone}`}>
              <span className="summary-card__label">Last successful backup</span>
              <strong className="summary-card__value summary-card__value--text">{freshness.value}</strong>
              <span className="summary-card__support">{formatTimestamp(cluster.lastSuccessfulBackup)}</span>
            </article>
            <article className="summary-card">
              <span className="summary-card__label">Backup health</span>
              <strong className="summary-card__value summary-card__value--text">{freshnessLabel}</strong>
              <span className="summary-card__support">Derived from the last successful backup age.</span>
            </article>
            <article className="summary-card">
              <span className="summary-card__label">History entries</span>
              <strong className="summary-card__value tabular-values">{orderedBackups.length}</strong>
            </article>
            <article className="summary-card">
              <span className="summary-card__label">Schedules tracked</span>
              <strong className="summary-card__value tabular-values">{orderedSchedules.length}</strong>
            </article>
            <article className="summary-card">
              <span className="summary-card__label">Cluster route</span>
              <strong className="summary-card__value summary-card__value--text">{backupsPath}</strong>
            </article>
          </div>

          <section className="backups-section" aria-labelledby="scheduled-backups-title">
            <div className="backups-section__header">
              <div>
                <p className="cluster-card__eyebrow">Scheduled backups</p>
                <h3 id="scheduled-backups-title">Schedule coverage for this cluster</h3>
              </div>
              <p className="backups-section__caption">
                Cron, target, and next-run timing come directly from the dedicated cluster backups endpoint.
              </p>
            </div>

            {orderedSchedules.length === 0 ? (
              <section className="state-card" aria-live="polite">
                <h4>No backup schedules tracked</h4>
                <p>This cluster does not currently expose any ScheduledBackup resources.</p>
              </section>
            ) : (
              <div className="backup-schedule-grid" aria-live="polite">
                {orderedSchedules.map((schedule) => {
                  const statusTone = scheduleStatusTone(schedule);
                  return (
                    <article key={`${schedule.namespace}/${schedule.name}`} className="detail-card backup-schedule-card">
                      <div className="detail-card__header">
                        <div>
                          <p className="cluster-card__eyebrow">{schedule.namespace}</p>
                          <h4>{schedule.name}</h4>
                        </div>
                        <span className={`detail-chip detail-chip--${statusTone}`}>{scheduleStatusLabel(schedule)}</span>
                      </div>

                      <dl className="cluster-metadata cluster-metadata--detail">
                        <div>
                          <dt>Cron schedule</dt>
                          <dd>{schedule.schedule}</dd>
                        </div>
                        <div>
                          <dt>Target cluster</dt>
                          <dd>{schedule.clusterName}</dd>
                        </div>
                        <div>
                          <dt>Method</dt>
                          <dd>{schedule.method || '—'}</dd>
                        </div>
                        <div>
                          <dt>Target</dt>
                          <dd>{schedule.target || '—'}</dd>
                        </div>
                        <div>
                          <dt>Last execution</dt>
                          <dd>{formatTimestamp(schedule.lastScheduleTime)}</dd>
                        </div>
                        <div>
                          <dt>Next scheduled run</dt>
                          <dd>{formatTimestamp(schedule.nextScheduleTime)}</dd>
                        </div>
                      </dl>
                    </article>
                  );
                })}
              </div>
            )}
          </section>

          <section className="backups-section" aria-labelledby="backup-history-title">
            <div className="backups-section__header">
              <div>
                <p className="cluster-card__eyebrow">Backup history</p>
                <h3 id="backup-history-title">Visible backup status, duration, and errors</h3>
              </div>
              <p className="backups-section__caption">
                Progress stays truthful by refetching the list when matching live events arrive instead of trusting socket payloads.
              </p>
            </div>

            {orderedBackups.length === 0 ? (
              <section className="state-card" aria-live="polite">
                <h4>No backup history yet</h4>
                <p>Use the backup trigger above or wait for the next scheduled run to populate this cluster history.</p>
              </section>
            ) : (
              <div className="backup-table-wrapper">
                <table className="backup-table">
                  <thead>
                    <tr>
                      <th scope="col">Name</th>
                      <th scope="col">Created</th>
                      <th scope="col">Status</th>
                      <th scope="col">Method</th>
                      <th scope="col">Target</th>
                      <th scope="col">Duration</th>
                      <th scope="col">Error</th>
                      <th scope="col">Restore</th>
                    </tr>
                  </thead>
                  <tbody>
                    {orderedBackups.map((backup) => {
                      const tone = phaseTone(backup.phase);
                      return (
                        <tr key={`${backup.namespace}/${backup.name}`}>
                          <th scope="row">{backup.name}</th>
                          <td>{formatTimestamp(backup.createdAt)}</td>
                          <td>
                            <span className={`detail-chip detail-chip--${tone}`}>{formatPhaseLabel(backup.phase)}</span>
                          </td>
                          <td>{backup.method || '—'}</td>
                          <td>{backup.target || '—'}</td>
                          <td>{formatDuration(backup.startedAt, backup.stoppedAt)}</td>
                          <td className={backup.error ? 'backup-table__error-cell' : ''}>{backup.error || '—'}</td>
                          <td>
                            <Link
                              className="secondary-button backups-table__action"
                              to={`${restorePath}?backup=${encodeURIComponent(backup.name)}`}
                            >
                              Restore from backup
                            </Link>
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </section>
        </>
      ) : null}
    </section>
  );
}
