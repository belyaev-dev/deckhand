import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useWebSocket } from './useWebSocket';
import type {
  ClusterBackupsResponse,
  ClusterSummary,
  CreateBackupRequest,
  CreateBackupResponse,
  ErrorResponse,
  WSChangeEvent,
} from '../types/api';

const LIVE_REFRESH_DEBOUNCE_MS = 150;

const EMPTY_CLUSTER: ClusterSummary = {
  namespace: '',
  name: '',
  desiredInstances: 0,
  readyInstances: 0,
};

const EMPTY_RESPONSE: ClusterBackupsResponse = {
  cluster: EMPTY_CLUSTER,
  backups: [],
  scheduledBackups: [],
};

export type BackupsRefreshReason = 'initial-load' | 'route-change' | 'live-update' | 'manual' | 'trigger-backup';

export interface UseBackupsResult {
  data: ClusterBackupsResponse;
  cluster: ClusterSummary;
  isLoading: boolean;
  isRefreshing: boolean;
  isSubmitting: boolean;
  error: string | null;
  submitError: string | null;
  triggerBackup: (request?: CreateBackupRequest) => Promise<CreateBackupResponse>;
  refetch: (reason?: BackupsRefreshReason) => void;
  lastLoadedAt: number | null;
  lastRefreshReason: BackupsRefreshReason;
  lastEvent: WSChangeEvent | null;
  liveUpdates: ReturnType<typeof useWebSocket>;
}

function buildBackupsURL(namespace: string, name: string) {
  return `/api/clusters/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/backups`;
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

function isMatchingLiveEvent(event: WSChangeEvent, namespace?: string, name?: string) {
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

export function useBackups(namespace?: string, name?: string): UseBackupsResult {
  const [data, setData] = useState<ClusterBackupsResponse>(EMPTY_RESPONSE);
  const [isLoading, setIsLoading] = useState(true);
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const [lastLoadedAt, setLastLoadedAt] = useState<number | null>(null);
  const [lastRefreshReason, setLastRefreshReason] = useState<BackupsRefreshReason>('initial-load');
  const [lastEvent, setLastEvent] = useState<WSChangeEvent | null>(null);
  const [refreshTick, setRefreshTick] = useState(0);

  const hasLoadedRef = useRef(false);
  const loadedClusterKeyRef = useRef<string | null>(null);
  const pendingReasonRef = useRef<BackupsRefreshReason>('initial-load');
  const liveRefreshTimerRef = useRef<number | null>(null);

  const clusterKey = namespace && name ? `${namespace}/${name}` : null;

  const refetch = useCallback((reason: BackupsRefreshReason = 'manual') => {
    pendingReasonRef.current = reason;
    setRefreshTick((value) => value + 1);
  }, []);

  useEffect(() => {
    return () => {
      if (liveRefreshTimerRef.current !== null) {
        window.clearTimeout(liveRefreshTimerRef.current);
      }
    };
  }, []);

  useEffect(() => {
    if (!clusterKey) {
      hasLoadedRef.current = false;
      loadedClusterKeyRef.current = null;
      setData(EMPTY_RESPONSE);
      setError('Missing cluster route parameters.');
      setSubmitError(null);
      setIsLoading(false);
      setIsRefreshing(false);
      setIsSubmitting(false);
      setLastLoadedAt(null);
      setLastEvent(null);
      setLastRefreshReason('initial-load');
      return;
    }

    const previousClusterKey = loadedClusterKeyRef.current;
    const isRouteChange = previousClusterKey !== null && previousClusterKey !== clusterKey;
    const currentReason: BackupsRefreshReason = !hasLoadedRef.current
      ? 'initial-load'
      : isRouteChange
        ? 'route-change'
        : pendingReasonRef.current;
    pendingReasonRef.current = 'manual';

    if (isRouteChange) {
      setLastEvent(null);
      setSubmitError(null);
    }

    const controller = new AbortController();
    const requestId = Symbol('backups-request');
    let activeRequest = requestId;

    setError(null);
    if (!hasLoadedRef.current || isRouteChange) {
      setIsLoading(true);
      setIsRefreshing(false);
      if (isRouteChange) {
        setData(EMPTY_RESPONSE);
      }
    } else {
      setIsRefreshing(true);
    }

    const load = async () => {
      try {
        const nextData = await fetchJSON<ClusterBackupsResponse>(buildBackupsURL(namespace!, name!), controller.signal);

        if (activeRequest !== requestId || controller.signal.aborted) {
          return;
        }

        setData(nextData);
        setLastLoadedAt(Date.now());
        setLastRefreshReason(currentReason);
        hasLoadedRef.current = true;
        loadedClusterKeyRef.current = clusterKey;
      } catch (loadError) {
        if (controller.signal.aborted) {
          return;
        }

        const message = loadError instanceof Error ? loadError.message : 'Failed to load backups.';
        setError(message);
        if (!hasLoadedRef.current || isRouteChange) {
          setData(EMPTY_RESPONSE);
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
  }, [clusterKey, name, namespace, refreshTick]);

  const triggerBackup = useCallback(async (request: CreateBackupRequest = {}) => {
    if (!clusterKey) {
      const missingRouteError = new Error('Missing cluster route parameters.');
      setSubmitError(missingRouteError.message);
      throw missingRouteError;
    }

    setIsSubmitting(true);
    setSubmitError(null);

    try {
      const response = await fetch(buildBackupsURL(namespace!, name!), {
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

      const payload = (await response.json()) as CreateBackupResponse;
      refetch('trigger-backup');
      return payload;
    } catch (submitFailure) {
      const message = submitFailure instanceof Error ? submitFailure.message : 'Failed to create a backup.';
      setSubmitError(message);
      throw submitFailure;
    } finally {
      setIsSubmitting(false);
    }
  }, [clusterKey, name, namespace, refetch]);

  const liveUpdates = useWebSocket({
    enabled: Boolean(clusterKey),
    onMessage: (event) => {
      if (!isMatchingLiveEvent(event, namespace, name)) {
        return;
      }

      setLastEvent(event);

      if (liveRefreshTimerRef.current !== null) {
        window.clearTimeout(liveRefreshTimerRef.current);
      }

      liveRefreshTimerRef.current = window.setTimeout(() => {
        refetch('live-update');
      }, LIVE_REFRESH_DEBOUNCE_MS);
    },
  });

  return useMemo<UseBackupsResult>(
    () => ({
      data,
      cluster: data.cluster,
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
    }),
    [data, error, isLoading, isRefreshing, isSubmitting, lastEvent, lastLoadedAt, lastRefreshReason, liveUpdates, refetch, submitError, triggerBackup],
  );
}
