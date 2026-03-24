import { useEffect, useMemo, useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router-dom';
import { useRestore } from '../hooks/useRestore';
import type { BackupSummary, RestoreStatus, WebSocketStatus } from '../types/api';

type RestoreStep = 'backup' | 'options' | 'confirm' | 'progress';
type TimelineState = 'complete' | 'current' | 'pending' | 'failed';

const STEP_ORDER: RestoreStep[] = ['backup', 'options', 'confirm', 'progress'];
const RESTORE_TIMELINE_PHASES = ['bootstrapping', 'recovering', 'ready', 'failed'] as const;
const DNS_1123_SUBDOMAIN_PATTERN = /^[a-z0-9](?:[-a-z0-9.]*[a-z0-9])?$/;

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

function formatRefreshReason(reason: string) {
  return reason.replace(/-/g, ' ');
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

function liveStatusDescription(
  status: WebSocketStatus,
  reconnectDelayMs: number | null,
  reconnectAttempt: number,
  hasTargetCluster: boolean,
) {
  switch (status) {
    case 'connected':
      return hasTargetCluster
        ? 'This page refetches restore status when a matching target-cluster store.changed event arrives.'
        : 'This page refetches restore options when matching source backup or cluster changes arrive.';
    case 'reconnecting': {
      const retryInSeconds = reconnectDelayMs ? Math.max(reconnectDelayMs / 1000, 1).toFixed(1) : 'soon';
      return `Retry ${reconnectAttempt} will start in ${retryInSeconds}s.`;
    }
    case 'error':
      return 'The last live updates attempt failed. Deckhand will retry automatically.';
    case 'disconnected':
      return 'Live updates are paused because the socket is disconnected.';
    default:
      return 'Connecting to the live updates stream for restore changes.';
  }
}

function defaultTargetName(name?: string) {
  if (!name) {
    return 'cluster-restore';
  }
  return `${name}-restore`;
}

function isPITRSupported(backup: BackupSummary | null) {
  return backup?.method === 'barmanObjectStore';
}

function validateDNS1123Subdomain(value: string, label: string) {
  const trimmed = value.trim();
  if (trimmed === '') {
    return `${label} is required.`;
  }
  if (trimmed.length > 253 || !DNS_1123_SUBDOMAIN_PATTERN.test(trimmed)) {
    return `${label} must be a lowercase RFC 1123 subdomain.`;
  }
  return null;
}

function parseRFC3339(value: string) {
  if (value.trim() === '') {
    return null;
  }

  const parsed = new Date(value);
  if (Number.isNaN(parsed.valueOf())) {
    return null;
  }

  return parsed;
}

function getPITRValidationError(
  backup: BackupSummary | null,
  pitrTargetTime: string,
  recoverabilityStart?: string | null,
  recoverabilityEnd?: string | null,
) {
  if (pitrTargetTime.trim() === '') {
    return null;
  }

  if (!isPITRSupported(backup)) {
    return `Backup ${backup?.name ?? 'selection'} does not support PITR target time.`;
  }

  const parsed = parseRFC3339(pitrTargetTime);
  if (!parsed) {
    return 'Enter PITR target time as an RFC3339 timestamp, for example 2026-03-24T11:15:00Z.';
  }

  if (!recoverabilityStart || !recoverabilityEnd) {
    return 'This source cluster does not advertise a recoverability window for PITR.';
  }

  const start = new Date(recoverabilityStart);
  const end = new Date(recoverabilityEnd);
  if (Number.isNaN(start.valueOf()) || Number.isNaN(end.valueOf())) {
    return 'The advertised recoverability window is invalid.';
  }

  if (parsed.valueOf() < start.valueOf() || parsed.valueOf() > end.valueOf()) {
    return `PITR target time must stay within ${recoverabilityStart} to ${recoverabilityEnd}.`;
  }

  return null;
}

function getTimelineState(phase: (typeof RESTORE_TIMELINE_PHASES)[number], status: RestoreStatus | null): TimelineState {
  if (!status) {
    return 'pending';
  }

  if (phase === 'failed') {
    return status.phase === 'failed' ? 'failed' : 'pending';
  }

  const timestamps = status.timestamps;
  const phaseToTimestamp: Record<string, string | null | undefined> = {
    bootstrapping: timestamps.bootstrappingStartedAt,
    recovering: timestamps.recoveringStartedAt,
    ready: timestamps.readyAt,
  };

  if (status.phase === phase) {
    return 'current';
  }

  if (phaseToTimestamp[phase]) {
    return 'complete';
  }

  return 'pending';
}

function getTimelineTimestamp(phase: (typeof RESTORE_TIMELINE_PHASES)[number], status: RestoreStatus | null) {
  if (!status) {
    return null;
  }

  switch (phase) {
    case 'bootstrapping':
      return status.timestamps.bootstrappingStartedAt;
    case 'recovering':
      return status.timestamps.recoveringStartedAt;
    case 'ready':
      return status.timestamps.readyAt;
    case 'failed':
      return status.timestamps.failedAt;
    default:
      return null;
  }
}

function buildStepButtonLabel(step: RestoreStep) {
  switch (step) {
    case 'backup':
      return 'Choose backup';
    case 'options':
      return 'Configure restore';
    case 'confirm':
      return 'Confirm restore';
    default:
      return 'Track progress';
  }
}

export default function Restore() {
  const params = useParams();
  const namespace = params.namespace;
  const name = params.name;
  const [searchParams, setSearchParams] = useSearchParams();

  const seededBackupName = searchParams.get('backup')?.trim() ?? '';
  const seededTargetNamespace = searchParams.get('targetNamespace')?.trim() ?? '';
  const seededTargetName = searchParams.get('targetName')?.trim() ?? '';
  const resumeTargetNamespace = seededTargetNamespace || undefined;
  const resumeTargetName = seededTargetName || undefined;

  const {
    options,
    sourceCluster,
    targetCluster,
    restoreResult,
    restoreStatus,
    isLoading,
    isRefreshing,
    isStatusLoading,
    isStatusRefreshing,
    isSubmitting,
    error,
    submitError,
    statusError,
    createRestore,
    refetch,
    lastLoadedAt,
    lastRefreshReason,
    lastEvent,
    liveUpdates,
  } = useRestore(namespace, name, {
    targetNamespace: resumeTargetNamespace,
    targetName: resumeTargetName,
  });

  const [selectedBackupName, setSelectedBackupName] = useState(seededBackupName);
  const [targetNamespace, setTargetNamespace] = useState(seededTargetNamespace || namespace || '');
  const [targetName, setTargetName] = useState(seededTargetName || defaultTargetName(name));
  const [pitrTargetTime, setPitrTargetTime] = useState('');
  const [currentStep, setCurrentStep] = useState<RestoreStep>(resumeTargetNamespace && resumeTargetName ? 'progress' : 'backup');
  const [showYAMLPreview, setShowYAMLPreview] = useState(false);

  useEffect(() => {
    setSelectedBackupName(seededBackupName);
    setTargetNamespace(resumeTargetNamespace || namespace || '');
    setTargetName(resumeTargetName || defaultTargetName(name));
    setPitrTargetTime('');
    setShowYAMLPreview(false);
    setCurrentStep(resumeTargetNamespace && resumeTargetName ? 'progress' : 'backup');
  }, [name, namespace, resumeTargetName, resumeTargetNamespace, seededBackupName]);

  useEffect(() => {
    if (options.backups.length === 0) {
      setSelectedBackupName('');
      return;
    }

    const hasSelectedBackup = options.backups.some((backup) => backup.name === selectedBackupName);
    if (hasSelectedBackup) {
      return;
    }

    const seededBackup = options.backups.find((backup) => backup.name === seededBackupName);
    setSelectedBackupName(seededBackup?.name ?? options.backups[0].name);
  }, [options.backups, seededBackupName, selectedBackupName]);

  const selectedBackup = useMemo(
    () => options.backups.find((backup) => backup.name === selectedBackupName) ?? null,
    [options.backups, selectedBackupName],
  );

  const backupsPath = namespace && name
    ? `/clusters/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/backups`
    : '/';
  const clusterPath = namespace && name
    ? `/clusters/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`
    : '/';
  const targetClusterPath = targetCluster
    ? `/clusters/${encodeURIComponent(targetCluster.namespace)}/${encodeURIComponent(targetCluster.name)}`
    : null;
  const recoverabilityStart = options.recoverability.start ?? sourceCluster.firstRecoverabilityPoint ?? null;
  const recoverabilityEnd = options.recoverability.end ?? sourceCluster.lastSuccessfulBackup ?? null;
  const pitrValidationError = getPITRValidationError(
    selectedBackup,
    pitrTargetTime,
    recoverabilityStart,
    recoverabilityEnd,
  );
  const namespaceValidationError = validateDNS1123Subdomain(targetNamespace, 'Target namespace');
  const nameValidationError = validateDNS1123Subdomain(targetName, 'Target cluster name');
  const overwriteError = namespaceValidationError || nameValidationError
    ? null
    : targetNamespace.trim() === namespace && targetName.trim() === name
      ? 'Restore must create a new cluster instead of overwriting the source cluster.'
      : null;
  const canAdvanceFromBackup = selectedBackup !== null;
  const canAdvanceFromOptions = selectedBackup !== null
    && !namespaceValidationError
    && !nameValidationError
    && !overwriteError
    && !pitrValidationError;
  const hasTargetCluster = Boolean(targetCluster);

  const liveDescription = liveStatusDescription(
    liveUpdates.status,
    liveUpdates.reconnectDelayMs,
    liveUpdates.reconnectAttempt,
    hasTargetCluster,
  );

  const handleSelectBackup = (backupName: string) => {
    setSelectedBackupName(backupName);
    const nextParams = new URLSearchParams(searchParams);
    nextParams.set('backup', backupName);
    setSearchParams(nextParams, { replace: true });
  };

  const handleCreateRestore = async () => {
    if (!selectedBackup || !canAdvanceFromOptions) {
      return;
    }

    try {
      const response = await createRestore({
        backupName: selectedBackup.name,
        targetNamespace: targetNamespace.trim(),
        targetName: targetName.trim(),
        pitrTargetTime: pitrTargetTime.trim() || undefined,
      });

      const nextParams = new URLSearchParams(searchParams);
      nextParams.set('backup', response.backup.name);
      nextParams.set('targetNamespace', response.targetCluster.namespace);
      nextParams.set('targetName', response.targetCluster.name);
      setSearchParams(nextParams, { replace: true });
      setCurrentStep('progress');
    } catch {
      // The hook already exposes the surfaced submit error.
    }
  };

  if (!namespace || !name) {
    return (
      <section className="state-card state-card--error" aria-live="assertive">
        <h2>Restore route is incomplete</h2>
        <p>Deckhand needs both a namespace and cluster name to prepare a guided restore.</p>
      </section>
    );
  }

  return (
    <section className="restore-page" aria-labelledby="restore-page-title">
      <div className="restore-page__hero">
        <div className="cluster-detail-page__intro restore-page__intro">
          <div className="cluster-detail-page__actions">
            <Link className="secondary-button cluster-detail-page__back-link" to={backupsPath}>
              Back to backups
            </Link>
            <Link className="secondary-button cluster-detail-page__back-link" to={clusterPath}>
              Back to cluster detail
            </Link>
          </div>
          <p className="eyebrow">{sourceCluster.namespace || namespace}</p>
          <h2 id="restore-page-title">Restore {sourceCluster.name || name} into a new cluster</h2>
          <p className="lede">
            Choose a completed backup, optionally pin a PITR target time inside the advertised recovery window,
            and let Deckhand create a new CloudNativePG cluster without overwriting the source.
          </p>
          <div className="restore-page__headline-row">
            <span className="detail-chip detail-chip--warning">Creates a new cluster</span>
            {isRefreshing ? <span className="refresh-state">Refreshing restore options…</span> : null}
            {isStatusRefreshing ? <span className="refresh-state">Refreshing restore status…</span> : null}
            {isSubmitting ? <span className="refresh-state">Submitting restore…</span> : null}
          </div>
        </div>

        <aside className={`live-status live-status--${liveUpdates.status}`} aria-live="polite">
          <div className="live-status__label-row">
            <span className="live-status__label">Restore live updates</span>
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
              <dd>{formatRefreshReason(lastRefreshReason)}</dd>
            </div>
            <div>
              <dt>Last matching event</dt>
              <dd>
                {lastEvent
                  ? `${lastEvent.kind} ${lastEvent.action} ${lastEvent.namespace}/${lastEvent.name}`
                  : hasTargetCluster
                    ? 'Waiting for a matching target-cluster change'
                    : 'Waiting for a matching source backup or cluster change'}
              </dd>
            </div>
          </dl>
          {liveUpdates.lastError ? <p className="live-status__error">{liveUpdates.lastError}</p> : null}
        </aside>
      </div>

      <nav className="restore-stepper" aria-label="Restore steps">
        {STEP_ORDER.map((step) => {
          const stepIndex = STEP_ORDER.indexOf(step);
          const currentStepIndex = STEP_ORDER.indexOf(currentStep);
          const stepState = step === currentStep
            ? 'current'
            : stepIndex < currentStepIndex
              ? 'complete'
              : 'pending';
          return (
            <div key={step} className={`restore-stepper__item restore-stepper__item--${stepState}`}>
              <span className="restore-stepper__index">{stepIndex + 1}</span>
              <div>
                <strong>{buildStepButtonLabel(step)}</strong>
                <p>{step === 'progress' ? 'Watch the target cluster move toward readiness.' : 'Continue through the guided workflow.'}</p>
              </div>
            </div>
          );
        })}
      </nav>

      {error ? (
        <section className="state-card state-card--error" aria-live="assertive">
          <h3>Could not load restore options</h3>
          <p>{error}</p>
          <button className="secondary-button" type="button" onClick={() => refetch('manual')}>
            Retry request
          </button>
        </section>
      ) : null}

      {submitError ? (
        <section className="state-card state-card--error" aria-live="assertive">
          <h3>Restore request failed</h3>
          <p>{submitError}</p>
        </section>
      ) : null}

      {statusError ? (
        <section className="state-card state-card--error" aria-live="assertive">
          <h3>Restore status refresh failed</h3>
          <p>{statusError}</p>
          <p>Prior restore context stays visible so you can keep investigating the target cluster.</p>
        </section>
      ) : null}

      {isLoading ? (
        <section className="state-card" aria-live="polite">
          <h3>Loading restore workflow</h3>
          <p>Deckhand is fetching /api/clusters/{namespace}/{name}/restore.</p>
        </section>
      ) : null}

      {!isLoading && !error ? (
        <>
          <div className="summary-grid summary-grid--detail" aria-label="Restore workflow summary">
            <article className="summary-card">
              <span className="summary-card__label">Source cluster</span>
              <strong className="summary-card__value summary-card__value--text">{sourceCluster.namespace || namespace}/{sourceCluster.name || name}</strong>
            </article>
            <article className="summary-card">
              <span className="summary-card__label">Completed backups</span>
              <strong className="summary-card__value tabular-values">{options.backups.length}</strong>
            </article>
            <article className="summary-card">
              <span className="summary-card__label">Recoverability start</span>
              <strong className="summary-card__value summary-card__value--text">{formatTimestamp(recoverabilityStart)}</strong>
            </article>
            <article className="summary-card">
              <span className="summary-card__label">Recoverability end</span>
              <strong className="summary-card__value summary-card__value--text">{formatTimestamp(recoverabilityEnd)}</strong>
            </article>
            <article className="summary-card">
              <span className="summary-card__label">Restore phases</span>
              <strong className="summary-card__value summary-card__value--text">{options.supportedPhases.map((phase) => formatPhaseLabel(phase)).join(' → ')}</strong>
            </article>
          </div>

          {currentStep === 'backup' ? (
            <section className="restore-panel" aria-labelledby="restore-backup-step-title">
              <div className="restore-panel__header">
                <div>
                  <p className="cluster-card__eyebrow">Step 1</p>
                  <h3 id="restore-backup-step-title">Choose the backup that becomes the restore source</h3>
                </div>
                <p className="restore-panel__caption">
                  The selected backup determines the restore catalog and whether PITR target time is available.
                </p>
              </div>

              <section className="restore-warning" aria-label="Restore warning">
                <strong>Restore safety note</strong>
                <p>This workflow creates a new cluster resource. The existing source cluster is never overwritten.</p>
              </section>

              {options.backups.length === 0 ? (
                <section className="state-card" aria-live="polite">
                  <h4>No completed backups are available</h4>
                  <p>Wait for a completed backup before creating a restore target for {sourceCluster.name || name}.</p>
                </section>
              ) : (
                <fieldset className="restore-backup-list">
                  <legend className="field__label">Completed backups</legend>
                  {options.backups.map((backup) => {
                    const isSelected = backup.name === selectedBackupName;
                    return (
                      <label
                        key={`${backup.namespace}/${backup.name}`}
                        className={`restore-backup-card${isSelected ? ' restore-backup-card--selected' : ''}`}
                      >
                        <input
                          type="radio"
                          name="restore-backup"
                          value={backup.name}
                          checked={isSelected}
                          onChange={() => handleSelectBackup(backup.name)}
                        />
                        <div>
                          <div className="restore-backup-card__header">
                            <strong>{backup.name}</strong>
                            <span className="detail-chip detail-chip--neutral">{formatPhaseLabel(backup.phase)}</span>
                          </div>
                          <dl className="cluster-metadata cluster-metadata--detail">
                            <div>
                              <dt>Created</dt>
                              <dd>{formatTimestamp(backup.createdAt)}</dd>
                            </div>
                            <div>
                              <dt>Method</dt>
                              <dd>{backup.method || '—'}</dd>
                            </div>
                            <div>
                              <dt>Target</dt>
                              <dd>{backup.target || '—'}</dd>
                            </div>
                            <div>
                              <dt>PITR support</dt>
                              <dd>{isPITRSupported(backup) ? 'Supported inside advertised window' : 'Not supported for this backup method'}</dd>
                            </div>
                          </dl>
                        </div>
                      </label>
                    );
                  })}
                </fieldset>
              )}

              <div className="control-panel__actions">
                <button className="secondary-button" type="button" onClick={() => refetch('manual')}>
                  Refresh restore options
                </button>
                <button className="primary-button" type="button" onClick={() => setCurrentStep('options')} disabled={!canAdvanceFromBackup}>
                  Continue to restore settings
                </button>
              </div>
            </section>
          ) : null}

          {currentStep === 'options' ? (
            <section className="restore-panel" aria-labelledby="restore-options-step-title">
              <div className="restore-panel__header">
                <div>
                  <p className="cluster-card__eyebrow">Step 2</p>
                  <h3 id="restore-options-step-title">Configure the new target cluster and optional PITR</h3>
                </div>
                <p className="restore-panel__caption">
                  Target namespace and name are validated client-side before Deckhand submits the restore request.
                </p>
              </div>

              <div className="restore-form-grid">
                <label className="field">
                  <span className="field__label">Target namespace</span>
                  <input
                    className="restore-input"
                    name="target-namespace"
                    value={targetNamespace}
                    onChange={(event) => setTargetNamespace(event.target.value)}
                  />
                  {namespaceValidationError ? <span className="restore-field-error">{namespaceValidationError}</span> : null}
                </label>

                <label className="field">
                  <span className="field__label">Target cluster name</span>
                  <input
                    className="restore-input"
                    name="target-name"
                    value={targetName}
                    onChange={(event) => setTargetName(event.target.value)}
                  />
                  {nameValidationError ? <span className="restore-field-error">{nameValidationError}</span> : null}
                </label>
              </div>

              {overwriteError ? <p className="restore-field-error">{overwriteError}</p> : null}

              <section className="restore-pitr-card" aria-labelledby="restore-pitr-title">
                <div className="restore-panel__header">
                  <div>
                    <p className="cluster-card__eyebrow">Optional PITR</p>
                    <h4 id="restore-pitr-title">Pin a target time inside the advertised recoverability window</h4>
                  </div>
                </div>
                <p className="restore-panel__caption">
                  Window: {recoverabilityStart ? recoverabilityStart : '—'} → {recoverabilityEnd ? recoverabilityEnd : '—'}
                </p>
                <label className="field">
                  <span className="field__label">PITR target time (RFC3339)</span>
                  <input
                    className="restore-input"
                    aria-label="PITR target time"
                    placeholder="2026-03-24T11:15:00Z"
                    value={pitrTargetTime}
                    onChange={(event) => setPitrTargetTime(event.target.value)}
                    disabled={!isPITRSupported(selectedBackup)}
                  />
                </label>
                {!isPITRSupported(selectedBackup) ? (
                  <p className="restore-panel__caption">This backup method does not support PITR target time.</p>
                ) : null}
                {pitrValidationError ? <p className="restore-field-error">{pitrValidationError}</p> : null}
              </section>

              <div className="control-panel__actions">
                <button className="secondary-button" type="button" onClick={() => setCurrentStep('backup')}>
                  Back to backup selection
                </button>
                <button className="primary-button" type="button" onClick={() => setCurrentStep('confirm')} disabled={!canAdvanceFromOptions}>
                  Continue to confirmation
                </button>
              </div>
            </section>
          ) : null}

          {currentStep === 'confirm' ? (
            <section className="restore-panel" aria-labelledby="restore-confirm-step-title">
              <div className="restore-panel__header">
                <div>
                  <p className="cluster-card__eyebrow">Step 3</p>
                  <h3 id="restore-confirm-step-title">Confirm the restore request</h3>
                </div>
                <p className="restore-panel__caption">
                  Deckhand will submit the real restore create request. The exact YAML preview becomes available from the accepted response.
                </p>
              </div>

              <section className="restore-warning" aria-label="New cluster warning">
                <strong>Important</strong>
                <p>
                  Submitting this step creates <code>{targetNamespace.trim()}/{targetName.trim()}</code>. It does not modify{' '}
                  <code>{namespace}/{name}</code>.
                </p>
              </section>

              <dl className="cluster-metadata cluster-metadata--detail restore-confirm-grid">
                <div>
                  <dt>Source cluster</dt>
                  <dd>{namespace}/{name}</dd>
                </div>
                <div>
                  <dt>Backup</dt>
                  <dd>{selectedBackup?.name ?? '—'}</dd>
                </div>
                <div>
                  <dt>Target cluster</dt>
                  <dd>{targetNamespace.trim()}/{targetName.trim()}</dd>
                </div>
                <div>
                  <dt>PITR target time</dt>
                  <dd>{pitrTargetTime.trim() || 'Restore to the latest recoverable point'}</dd>
                </div>
              </dl>

              {pitrValidationError || overwriteError || namespaceValidationError || nameValidationError ? (
                <section className="state-card state-card--error" aria-live="assertive">
                  <h4>Resolve validation issues before submitting</h4>
                  <p>{pitrValidationError || overwriteError || namespaceValidationError || nameValidationError}</p>
                </section>
              ) : null}

              <div className="control-panel__actions">
                <button className="secondary-button" type="button" onClick={() => setCurrentStep('options')}>
                  Back to restore settings
                </button>
                <button className="primary-button" type="button" onClick={() => void handleCreateRestore()} disabled={!canAdvanceFromOptions || isSubmitting}>
                  {isSubmitting ? 'Creating restore…' : 'Create restore cluster'}
                </button>
              </div>
            </section>
          ) : null}

          {currentStep === 'progress' ? (
            <section className="restore-panel" aria-labelledby="restore-progress-step-title">
              <div className="restore-panel__header">
                <div>
                  <p className="cluster-card__eyebrow">Step 4</p>
                  <h3 id="restore-progress-step-title">Monitor the target cluster</h3>
                </div>
                <p className="restore-panel__caption">
                  Status stays truthful by refetching the target cluster on matching store.changed events instead of trusting socket payloads.
                </p>
              </div>

              {isStatusLoading && !restoreStatus ? (
                <section className="state-card" aria-live="polite">
                  <h4>Loading restore progress</h4>
                  <p>Deckhand is fetching /api/clusters/{resumeTargetNamespace || targetCluster?.namespace}/{resumeTargetName || targetCluster?.name}/restore-status.</p>
                </section>
              ) : null}

              {targetCluster ? (
                <div className="restore-progress-grid">
                  <article className="detail-card restore-progress-card">
                    <div className="detail-card__header">
                      <div>
                        <p className="cluster-card__eyebrow">Current target</p>
                        <h4>{targetCluster.namespace}/{targetCluster.name}</h4>
                      </div>
                      <span className={`detail-chip detail-chip--${restoreStatus?.phase === 'failed' ? 'critical' : restoreStatus?.phase === 'ready' ? 'healthy' : 'warning'}`}>
                        {formatPhaseLabel(restoreStatus?.phase)}
                      </span>
                    </div>
                    <dl className="cluster-metadata cluster-metadata--detail">
                      <div>
                        <dt>Phase reason</dt>
                        <dd>{restoreStatus?.phaseReason || '—'}</dd>
                      </div>
                      <div>
                        <dt>Last transition</dt>
                        <dd>{formatTimestamp(restoreStatus?.timestamps.lastTransitionAt)}</dd>
                      </div>
                      <div>
                        <dt>Message</dt>
                        <dd>{restoreStatus?.message || 'Waiting for Deckhand to observe the target cluster.'}</dd>
                      </div>
                      <div>
                        <dt>Last surfaced error</dt>
                        <dd>{restoreStatus?.error || '—'}</dd>
                      </div>
                    </dl>
                    <div className="control-panel__actions">
                      <button className="secondary-button" type="button" onClick={() => refetch('manual')}>
                        Refresh status now
                      </button>
                      {targetClusterPath ? (
                        <Link className="secondary-button cluster-detail-page__back-link" to={targetClusterPath}>
                          Open target cluster
                        </Link>
                      ) : null}
                    </div>
                  </article>

                  <article className="detail-card restore-progress-card">
                    <div className="detail-card__header">
                      <div>
                        <p className="cluster-card__eyebrow">Timeline</p>
                        <h4>Restore phase progression</h4>
                      </div>
                    </div>
                    <ol className="restore-timeline">
                      {RESTORE_TIMELINE_PHASES.map((phase) => {
                        const state = getTimelineState(phase, restoreStatus);
                        return (
                          <li key={phase} className={`restore-timeline__item restore-timeline__item--${state}`}>
                            <div className="restore-timeline__marker" aria-hidden="true" />
                            <div>
                              <div className="restore-timeline__row">
                                <strong>{formatPhaseLabel(phase)}</strong>
                                <span>{formatTimestamp(getTimelineTimestamp(phase, restoreStatus))}</span>
                              </div>
                              <p>
                                {phase === 'bootstrapping' ? 'Cluster resource accepted and bootstrapping has started.' : null}
                                {phase === 'recovering' ? 'CloudNativePG is applying restore and recovery work.' : null}
                                {phase === 'ready' ? 'The target cluster reported readiness.' : null}
                                {phase === 'failed' ? 'Deckhand surfaced a restore failure from the watched target cluster.' : null}
                              </p>
                            </div>
                          </li>
                        );
                      })}
                    </ol>
                  </article>
                </div>
              ) : (
                <section className="state-card" aria-live="polite">
                  <h4>No target cluster is being tracked yet</h4>
                  <p>Submit the restore request to switch this page into live progress mode.</p>
                </section>
              )}

              <article className="detail-card restore-yaml-card">
                <div className="detail-card__header">
                  <div>
                    <p className="cluster-card__eyebrow">YAML preview</p>
                    <h4>Exact manifest returned by the backend</h4>
                  </div>
                  {restoreResult?.yamlPreview ? (
                    <button className="secondary-button" type="button" onClick={() => setShowYAMLPreview((value) => !value)}>
                      {showYAMLPreview ? 'Hide YAML' : 'Show YAML'}
                    </button>
                  ) : null}
                </div>
                {restoreResult?.yamlPreview ? (
                  showYAMLPreview ? <pre className="restore-yaml-preview">{restoreResult.yamlPreview}</pre> : <p className="restore-panel__caption">Toggle the YAML preview to inspect the exact Cluster manifest accepted by the backend.</p>
                ) : (
                  <p className="restore-panel__caption">
                    YAML preview is available immediately after a successful create response. If you refreshed later, use the target cluster detail view for ongoing runtime truth.
                  </p>
                )}
              </article>
            </section>
          ) : null}
        </>
      ) : null}
    </section>
  );
}
