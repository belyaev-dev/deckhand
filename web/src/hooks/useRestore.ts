import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useWebSocket } from './useWebSocket';
import type {
  ClusterRestoreOptionsResponse,
  ClusterSummary,
  CreateRestoreRequest,
  CreateRestoreResponse,
  ErrorResponse,
  RestoreStatus,
  WSChangeEvent,
} from '../types/api';

const LIVE_REFRESH_DEBOUNCE_MS = 150;

const EMPTY_CLUSTER: ClusterSummary = {
  namespace: '',
  name: '',
  desiredInstances: 0,
  readyInstances: 0,
};

const EMPTY_OPTIONS_RESPONSE: ClusterRestoreOptionsResponse = {
  cluster: EMPTY_CLUSTER,
  backups: [],
  recoverability: {},
  supportedPhases: [],
};

export type RestoreRefreshReason =
  | 'initial-load'
  | 'route-change'
  | 'manual'
  | 'source-live-update'
  | 'create-accepted'
  | 'target-live-update';

export interface UseRestoreOptions {
  targetNamespace?: string;
  targetName?: string;
}

export interface UseRestoreResult {
  options: ClusterRestoreOptionsResponse;
  sourceCluster: ClusterSummary;
  targetCluster: ClusterSummary | null;
  restoreResult: CreateRestoreResponse | null;
  restoreStatus: RestoreStatus | null;
  isLoading: boolean;
  isRefreshing: boolean;
  isStatusLoading: boolean;
  isStatusRefreshing: boolean;
  isSubmitting: boolean;
  error: string | null;
  submitError: string | null;
  statusError: string | null;
  createRestore: (request: CreateRestoreRequest) => Promise<CreateRestoreResponse>;
  refetch: (reason?: RestoreRefreshReason) => void;
  lastLoadedAt: number | null;
  lastRefreshReason: RestoreRefreshReason;
  lastEvent: WSChangeEvent | null;
  liveUpdates: ReturnType<typeof useWebSocket>;
}

function buildRestoreOptionsURL(namespace: string, name: string) {
  return `/api/clusters/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/restore`;
}

function buildRestoreStatusURL(namespace: string, name: string) {
  return `/api/clusters/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/restore-status`;
}

async function readErrorMessage(response: Response) {
  const contentType = response.headers.get('content-type') ?? '';
  if (contentType.includes('application/json')) {
    const payload = (await response.json()) as ErrorResponse;
    if (typeof payload.error === 'string' && payload.error.trim() !== '') {
      return payload.error;
    }
  }

  const text = await response.text();
  if (text.trim() !== '') {
    return text;
  }

  return `Request failed with status ${response.status}.`;
}

async function fetchJSON<T>(url: string, signal: AbortSignal) {
  const response = await fetch(url, {
    headers: {
      Accept: 'application/json',
    },
    signal,
  });

  if (!response.ok) {
    throw new Error(await readErrorMessage(response));
  }

  return (await response.json()) as T;
}

function isMatchingSourceEvent(event: WSChangeEvent, namespace?: string, name?: string) {
  if (event.type !== 'store.changed' || event.namespace !== namespace) {
    return false;
  }

  switch (event.kind) {
    case 'backup':
    case 'scheduled_backup':
      return true;
    case 'cluster':
      return event.name === name;
    default:
      return false;
  }
}

function isMatchingTargetEvent(event: WSChangeEvent, namespace?: string, name?: string) {
  return (
    event.type === 'store.changed'
    && event.kind === 'cluster'
    && event.namespace === namespace
    && event.name === name
  );
}

export function useRestore(
  namespace?: string,
  name?: string,
  { targetNamespace, targetName }: UseRestoreOptions = {},
): UseRestoreResult {
  const [options, setOptions] = useState<ClusterRestoreOptionsResponse>(EMPTY_OPTIONS_RESPONSE);
  const [restoreResult, setRestoreResult] = useState<CreateRestoreResponse | null>(null);
  const [restoreStatus, setRestoreStatus] = useState<RestoreStatus | null>(null);
  const [statusCluster, setStatusCluster] = useState<ClusterSummary | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [isStatusLoading, setIsStatusLoading] = useState(false);
  const [isStatusRefreshing, setIsStatusRefreshing] = useState(false);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const [statusError, setStatusError] = useState<string | null>(null);
  const [lastLoadedAt, setLastLoadedAt] = useState<number | null>(null);
  const [lastRefreshReason, setLastRefreshReason] = useState<RestoreRefreshReason>('initial-load');
  const [lastEvent, setLastEvent] = useState<WSChangeEvent | null>(null);
  const [optionsRefreshTick, setOptionsRefreshTick] = useState(0);
  const [statusRefreshTick, setStatusRefreshTick] = useState(0);

  const hasLoadedOptionsRef = useRef(false);
  const loadedSourceClusterKeyRef = useRef<string | null>(null);
  const pendingOptionsReasonRef = useRef<RestoreRefreshReason>('initial-load');
  const pendingStatusReasonRef = useRef<RestoreRefreshReason>('manual');
  const liveRefreshTimerRef = useRef<number | null>(null);

  const sourceClusterKey = namespace && name ? `${namespace}/${name}` : null;
  const effectiveTargetNamespace = restoreResult?.targetCluster.namespace ?? targetNamespace;
  const effectiveTargetName = restoreResult?.targetCluster.name ?? targetName;
  const targetClusterKey = effectiveTargetNamespace && effectiveTargetName
    ? `${effectiveTargetNamespace}/${effectiveTargetName}`
    : null;

  const refetch = useCallback((reason: RestoreRefreshReason = 'manual') => {
    if (targetClusterKey) {
      pendingStatusReasonRef.current = reason;
      setStatusRefreshTick((value) => value + 1);
      return;
    }

    pendingOptionsReasonRef.current = reason;
    setOptionsRefreshTick((value) => value + 1);
  }, [targetClusterKey]);

  useEffect(() => {
    return () => {
      if (liveRefreshTimerRef.current !== null) {
        window.clearTimeout(liveRefreshTimerRef.current);
      }
    };
  }, []);

  useEffect(() => {
    if (!sourceClusterKey) {
      hasLoadedOptionsRef.current = false;
      loadedSourceClusterKeyRef.current = null;
      setOptions(EMPTY_OPTIONS_RESPONSE);
      setRestoreResult(null);
      setRestoreStatus(null);
      setStatusCluster(null);
      setError('Missing cluster route parameters.');
      setSubmitError(null);
      setStatusError(null);
      setIsLoading(false);
      setIsRefreshing(false);
      setIsStatusLoading(false);
      setIsStatusRefreshing(false);
      setIsSubmitting(false);
      setLastLoadedAt(null);
      setLastEvent(null);
      setLastRefreshReason('initial-load');
      return;
    }

    const previousClusterKey = loadedSourceClusterKeyRef.current;
    const isRouteChange = previousClusterKey !== null && previousClusterKey !== sourceClusterKey;
    const currentReason: RestoreRefreshReason = !hasLoadedOptionsRef.current
      ? 'initial-load'
      : isRouteChange
        ? 'route-change'
        : pendingOptionsReasonRef.current;
    pendingOptionsReasonRef.current = 'manual';

    if (isRouteChange) {
      setOptions(EMPTY_OPTIONS_RESPONSE);
      setRestoreResult(null);
      setRestoreStatus(null);
      setStatusCluster(null);
      setSubmitError(null);
      setStatusError(null);
      setLastEvent(null);
    }

    const controller = new AbortController();
    const requestId = Symbol('restore-options-request');
    let activeRequest = requestId;

    setError(null);
    if (!hasLoadedOptionsRef.current || isRouteChange) {
      setIsLoading(true);
      setIsRefreshing(false);
    } else {
      setIsRefreshing(true);
    }

    const load = async () => {
      try {
        const nextOptions = await fetchJSON<ClusterRestoreOptionsResponse>(
          buildRestoreOptionsURL(namespace!, name!),
          controller.signal,
        );

        if (activeRequest !== requestId || controller.signal.aborted) {
          return;
        }

        setOptions(nextOptions);
        setLastLoadedAt(Date.now());
        setLastRefreshReason(currentReason);
        hasLoadedOptionsRef.current = true;
        loadedSourceClusterKeyRef.current = sourceClusterKey;
      } catch (loadError) {
        if (controller.signal.aborted) {
          return;
        }

        const message = loadError instanceof Error ? loadError.message : 'Failed to load restore options.';
        setError(message);
        if (!hasLoadedOptionsRef.current || isRouteChange) {
          setOptions(EMPTY_OPTIONS_RESPONSE);
        }
      } finally {
        if (activeRequest === requestId && !controller.signal.aborted) {
          setIsLoading(false);
          setIsRefreshing(false);
        }
      }
    };

    void load();

    return () => {
      activeRequest = Symbol('cancelled');
      controller.abort();
    };
  }, [sourceClusterKey, namespace, name, optionsRefreshTick]);

  useEffect(() => {
    if (!targetClusterKey || !effectiveTargetNamespace || !effectiveTargetName) {
      if (!restoreResult) {
        setRestoreStatus(null);
        setStatusCluster(null);
        setStatusError(null);
      }
      setIsStatusLoading(false);
      setIsStatusRefreshing(false);
      return;
    }

    const currentReason = pendingStatusReasonRef.current;
    pendingStatusReasonRef.current = 'manual';

    const controller = new AbortController();
    const requestId = Symbol('restore-status-request');
    let activeRequest = requestId;
    const hasPriorStatus = restoreStatus !== null || restoreResult !== null;

    setStatusError(null);
    if (!hasPriorStatus) {
      setIsStatusLoading(true);
      setIsStatusRefreshing(false);
    } else {
      setIsStatusRefreshing(true);
    }

    const load = async () => {
      try {
        const response = await fetchJSON<{ cluster: ClusterSummary; status: RestoreStatus }>(
          buildRestoreStatusURL(effectiveTargetNamespace, effectiveTargetName),
          controller.signal,
        );

        if (activeRequest !== requestId || controller.signal.aborted) {
          return;
        }

        setStatusCluster(response.cluster);
        setRestoreStatus(response.status);
        setLastLoadedAt(Date.now());
        setLastRefreshReason(currentReason);
      } catch (loadError) {
        if (controller.signal.aborted) {
          return;
        }

        const message = loadError instanceof Error ? loadError.message : 'Failed to load restore status.';
        setStatusError(message);
      } finally {
        if (activeRequest === requestId && !controller.signal.aborted) {
          setIsStatusLoading(false);
          setIsStatusRefreshing(false);
        }
      }
    };

    void load();

    return () => {
      activeRequest = Symbol('cancelled');
      controller.abort();
    };
  }, [effectiveTargetName, effectiveTargetNamespace, statusRefreshTick, targetClusterKey]);

  const createRestore = useCallback(async (request: CreateRestoreRequest) => {
    if (!sourceClusterKey) {
      const missingRouteError = new Error('Missing cluster route parameters.');
      setSubmitError(missingRouteError.message);
      throw missingRouteError;
    }

    setIsSubmitting(true);
    setSubmitError(null);
    setStatusError(null);

    try {
      const response = await fetch(buildRestoreOptionsURL(namespace!, name!), {
        method: 'POST',
        headers: {
          Accept: 'application/json',
          'Content-Type': 'application/json',
        },
        body: JSON.stringify(request),
      });

      if (!response.ok) {
        throw new Error(await readErrorMessage(response));
      }

      const payload = (await response.json()) as CreateRestoreResponse;
      setRestoreResult(payload);
      setStatusCluster(payload.targetCluster);
      setRestoreStatus(payload.restoreStatus);
      setLastLoadedAt(Date.now());
      setLastRefreshReason('create-accepted');
      return payload;
    } catch (submitFailure) {
      const message = submitFailure instanceof Error ? submitFailure.message : 'Failed to create restore cluster.';
      setSubmitError(message);
      throw submitFailure;
    } finally {
      setIsSubmitting(false);
    }
  }, [sourceClusterKey, namespace, name]);

  const liveUpdates = useWebSocket({
    enabled: Boolean(sourceClusterKey),
    onMessage: (event) => {
      if (targetClusterKey && isMatchingTargetEvent(event, effectiveTargetNamespace, effectiveTargetName)) {
        setLastEvent(event);

        if (liveRefreshTimerRef.current !== null) {
          window.clearTimeout(liveRefreshTimerRef.current);
        }

        liveRefreshTimerRef.current = window.setTimeout(() => {
          pendingStatusReasonRef.current = 'target-live-update';
          setStatusRefreshTick((value) => value + 1);
        }, LIVE_REFRESH_DEBOUNCE_MS);
        return;
      }

      if (!targetClusterKey && isMatchingSourceEvent(event, namespace, name)) {
        setLastEvent(event);

        if (liveRefreshTimerRef.current !== null) {
          window.clearTimeout(liveRefreshTimerRef.current);
        }

        liveRefreshTimerRef.current = window.setTimeout(() => {
          pendingOptionsReasonRef.current = 'source-live-update';
          setOptionsRefreshTick((value) => value + 1);
        }, LIVE_REFRESH_DEBOUNCE_MS);
      }
    },
  });

  return useMemo<UseRestoreResult>(
    () => ({
      options,
      sourceCluster: options.cluster,
      targetCluster: restoreResult?.targetCluster ?? statusCluster,
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
    }),
    [
      createRestore,
      error,
      isLoading,
      isRefreshing,
      isStatusLoading,
      isStatusRefreshing,
      isSubmitting,
      lastEvent,
      lastLoadedAt,
      lastRefreshReason,
      liveUpdates,
      options,
      refetch,
      restoreResult,
      restoreStatus,
      statusCluster,
      statusError,
      submitError,
    ],
  );
}
