import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useWebSocket } from './useWebSocket';
import type {
  ClusterListResponse,
  ClusterNamespaceSummary,
  ClusterOverviewSummary,
  WSChangeEvent,
} from '../types/api';

const EMPTY_RESPONSE: ClusterListResponse = {
  namespaces: [],
  items: [],
};

const LIVE_REFRESH_DEBOUNCE_MS = 150;

type RefreshReason = 'initial-load' | 'filter-change' | 'live-update' | 'manual';

interface ErrorResponse {
  error?: string;
}

export interface UseClustersResult {
  data: ClusterListResponse;
  items: ClusterOverviewSummary[];
  namespaces: ClusterNamespaceSummary[];
  selectedNamespace: string;
  setSelectedNamespace: (namespace: string) => void;
  isLoading: boolean;
  isRefreshing: boolean;
  error: string | null;
  refetch: (reason?: RefreshReason) => void;
  lastLoadedAt: number | null;
  lastRefreshReason: RefreshReason;
  lastEvent: WSChangeEvent | null;
  liveUpdates: ReturnType<typeof useWebSocket>;
}

function buildClustersURL(namespace: string) {
  if (!namespace) {
    return '/api/clusters';
  }

  const params = new URLSearchParams({ namespace });
  return `/api/clusters?${params.toString()}`;
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

async function fetchClusters(namespace: string, signal: AbortSignal) {
  const response = await fetch(buildClustersURL(namespace), {
    headers: {
      Accept: 'application/json',
    },
    signal,
  });

  if (!response.ok) {
    throw new Error(await readErrorMessage(response));
  }

  return (await response.json()) as ClusterListResponse;
}

export function useClusters(): UseClustersResult {
  const [data, setData] = useState<ClusterListResponse>(EMPTY_RESPONSE);
  const [selectedNamespaceValue, setSelectedNamespaceValue] = useState('');
  const [isLoading, setIsLoading] = useState(true);
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [lastLoadedAt, setLastLoadedAt] = useState<number | null>(null);
  const [lastRefreshReason, setLastRefreshReason] = useState<RefreshReason>('initial-load');
  const [lastEvent, setLastEvent] = useState<WSChangeEvent | null>(null);
  const [refreshTick, setRefreshTick] = useState(0);

  const hasLoadedRef = useRef(false);
  const latestDataRef = useRef<ClusterListResponse>(EMPTY_RESPONSE);
  const liveRefreshTimerRef = useRef<number | null>(null);
  const pendingReasonRef = useRef<RefreshReason>('initial-load');

  const setSelectedNamespace = useCallback((namespace: string) => {
    pendingReasonRef.current = 'filter-change';
    setSelectedNamespaceValue(namespace);
  }, []);

  const refetch = useCallback((reason: RefreshReason = 'manual') => {
    pendingReasonRef.current = reason;
    setRefreshTick((value) => value + 1);
  }, []);

  useEffect(() => {
    latestDataRef.current = data;
  }, [data]);

  useEffect(() => {
    const requestId = Symbol('clusters-request');
    const controller = new AbortController();
    let activeRequest = requestId;

    const currentReason = pendingReasonRef.current;
    pendingReasonRef.current = 'manual';

    setError(null);
    if (!hasLoadedRef.current) {
      setIsLoading(true);
    } else {
      setIsRefreshing(true);
    }

    const load = async () => {
      try {
        const [scopedResult, namespacesResult] = await Promise.allSettled([
          fetchClusters(selectedNamespaceValue, controller.signal),
          selectedNamespaceValue
            ? fetchClusters('', controller.signal)
            : Promise.resolve<ClusterListResponse | null>(null),
        ]);

        if (activeRequest !== requestId || controller.signal.aborted) {
          return;
        }

        if (scopedResult.status === 'rejected') {
          throw scopedResult.reason;
        }

        const namespaces =
          namespacesResult.status === 'fulfilled' && namespacesResult.value
            ? namespacesResult.value.namespaces
            : selectedNamespaceValue
              ? latestDataRef.current.namespaces
              : scopedResult.value.namespaces;

        const nextData: ClusterListResponse = {
          items: scopedResult.value.items,
          namespaces,
        };

        setData(nextData);
        setLastLoadedAt(Date.now());
        setLastRefreshReason(currentReason);
        hasLoadedRef.current = true;
      } catch (loadError) {
        if (controller.signal.aborted) {
          return;
        }

        const message = loadError instanceof Error ? loadError.message : 'Failed to load clusters.';
        setError(message);
        if (!hasLoadedRef.current) {
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
  }, [refreshTick, selectedNamespaceValue]);

  useEffect(() => {
    return () => {
      if (liveRefreshTimerRef.current !== null) {
        window.clearTimeout(liveRefreshTimerRef.current);
      }
    };
  }, []);

  const liveUpdates = useWebSocket({
    onMessage: (event) => {
      if (event.type !== 'store.changed') {
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

  const result = useMemo<UseClustersResult>(
    () => ({
      data,
      items: data.items,
      namespaces: data.namespaces,
      selectedNamespace: selectedNamespaceValue,
      setSelectedNamespace,
      isLoading,
      isRefreshing,
      error,
      refetch,
      lastLoadedAt,
      lastRefreshReason,
      lastEvent,
      liveUpdates,
    }),
    [
      data,
      error,
      isLoading,
      isRefreshing,
      lastEvent,
      lastLoadedAt,
      lastRefreshReason,
      liveUpdates,
      refetch,
      selectedNamespaceValue,
      setSelectedNamespace,
    ],
  );

  return result;
}
