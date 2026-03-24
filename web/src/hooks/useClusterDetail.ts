import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useWebSocket } from './useWebSocket';
import type {
  ClusterDetailResponse,
  ClusterMetricsResponse,
  ClusterSummary,
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

const EMPTY_DETAIL_RESPONSE: ClusterDetailResponse = {
  cluster: EMPTY_CLUSTER,
  backups: [],
  scheduledBackups: [],
};

const EMPTY_METRICS_RESPONSE: ClusterMetricsResponse = {
  cluster: EMPTY_CLUSTER,
  overallHealth: 'unknown',
  scrapeError: 'metrics not available yet',
  instances: [],
};

export type ClusterDetailRefreshReason = 'initial-load' | 'route-change' | 'live-update' | 'manual';

export interface UseClusterDetailResult {
  detail: ClusterDetailResponse;
  metrics: ClusterMetricsResponse;
  cluster: ClusterSummary;
  isLoading: boolean;
  isRefreshing: boolean;
  error: string | null;
  refetch: (reason?: ClusterDetailRefreshReason) => void;
  lastLoadedAt: number | null;
  lastRefreshReason: ClusterDetailRefreshReason;
  lastEvent: WSChangeEvent | null;
  liveUpdates: ReturnType<typeof useWebSocket>;
}

function buildClusterDetailURL(namespace: string, name: string) {
  return `/api/clusters/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
}

function buildClusterMetricsURL(namespace: string, name: string) {
  return `${buildClusterDetailURL(namespace, name)}/metrics`;
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

export function useClusterDetail(namespace?: string, name?: string): UseClusterDetailResult {
  const [detail, setDetail] = useState<ClusterDetailResponse>(EMPTY_DETAIL_RESPONSE);
  const [metrics, setMetrics] = useState<ClusterMetricsResponse>(EMPTY_METRICS_RESPONSE);
  const [isLoading, setIsLoading] = useState(true);
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [lastLoadedAt, setLastLoadedAt] = useState<number | null>(null);
  const [lastRefreshReason, setLastRefreshReason] = useState<ClusterDetailRefreshReason>('initial-load');
  const [lastEvent, setLastEvent] = useState<WSChangeEvent | null>(null);
  const [refreshTick, setRefreshTick] = useState(0);

  const hasLoadedRef = useRef(false);
  const loadedClusterKeyRef = useRef<string | null>(null);
  const pendingReasonRef = useRef<ClusterDetailRefreshReason>('initial-load');
  const liveRefreshTimerRef = useRef<number | null>(null);

  const clusterKey = namespace && name ? `${namespace}/${name}` : null;

  const refetch = useCallback((reason: ClusterDetailRefreshReason = 'manual') => {
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
      setDetail(EMPTY_DETAIL_RESPONSE);
      setMetrics(EMPTY_METRICS_RESPONSE);
      setError('Missing cluster route parameters.');
      setIsLoading(false);
      setIsRefreshing(false);
      setLastLoadedAt(null);
      setLastEvent(null);
      setLastRefreshReason('initial-load');
      return;
    }

    const previousClusterKey = loadedClusterKeyRef.current;
    const isRouteChange = previousClusterKey !== null && previousClusterKey !== clusterKey;
    const currentReason: ClusterDetailRefreshReason = !hasLoadedRef.current
      ? 'initial-load'
      : isRouteChange
        ? 'route-change'
        : pendingReasonRef.current;
    pendingReasonRef.current = 'manual';

    if (isRouteChange) {
      setLastEvent(null);
    }

    const controller = new AbortController();
    const requestId = Symbol('cluster-detail-request');
    let activeRequest = requestId;

    setError(null);
    if (!hasLoadedRef.current || isRouteChange) {
      setIsLoading(true);
      setIsRefreshing(false);
      if (isRouteChange) {
        setDetail(EMPTY_DETAIL_RESPONSE);
        setMetrics(EMPTY_METRICS_RESPONSE);
      }
    } else {
      setIsRefreshing(true);
    }

    const load = async () => {
      try {
        const [nextDetail, nextMetrics] = await Promise.all([
          fetchJSON<ClusterDetailResponse>(buildClusterDetailURL(namespace!, name!), controller.signal),
          fetchJSON<ClusterMetricsResponse>(buildClusterMetricsURL(namespace!, name!), controller.signal),
        ]);

        if (activeRequest !== requestId || controller.signal.aborted) {
          return;
        }

        setDetail(nextDetail);
        setMetrics(nextMetrics);
        setLastLoadedAt(Date.now());
        setLastRefreshReason(currentReason);
        hasLoadedRef.current = true;
        loadedClusterKeyRef.current = clusterKey;
      } catch (loadError) {
        if (controller.signal.aborted) {
          return;
        }

        const message = loadError instanceof Error ? loadError.message : 'Failed to load cluster detail.';
        setError(message);
        if (!hasLoadedRef.current || isRouteChange) {
          setDetail(EMPTY_DETAIL_RESPONSE);
          setMetrics(EMPTY_METRICS_RESPONSE);
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

  const liveUpdates = useWebSocket({
    enabled: Boolean(clusterKey),
    onMessage: (event) => {
      if (
        event.type !== 'store.changed' ||
        event.namespace !== namespace ||
        event.name !== name
      ) {
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

  return useMemo<UseClusterDetailResult>(
    () => ({
      detail,
      metrics,
      cluster: detail.cluster.name ? detail.cluster : metrics.cluster,
      isLoading,
      isRefreshing,
      error,
      refetch,
      lastLoadedAt,
      lastRefreshReason,
      lastEvent,
      liveUpdates,
    }),
    [detail, error, isLoading, isRefreshing, lastEvent, lastLoadedAt, lastRefreshReason, liveUpdates, metrics, refetch],
  );
}
