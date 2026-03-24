import '@testing-library/jest-dom/vitest';
import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import ClusterDetail from './ClusterDetail';
import type { ClusterDetailResponse, ClusterMetricsResponse } from '../types/api';

class MockWebSocket {
  static instances: MockWebSocket[] = [];
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;

  url: string;
  readyState = MockWebSocket.CONNECTING;
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: (() => void) | null = null;

  constructor(url: string) {
    this.url = url;
    MockWebSocket.instances.push(this);
  }

  open() {
    this.readyState = MockWebSocket.OPEN;
    this.onopen?.();
  }

  emit(data: unknown) {
    this.onmessage?.({ data: JSON.stringify(data) });
  }

  close() {
    this.readyState = MockWebSocket.CLOSED;
    this.onclose?.();
  }

  send() {
    // no-op for tests
  }
}

function createJSONResponse(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: {
      'Content-Type': 'application/json',
    },
  });
}

function renderDetailRoute() {
  return render(
    <MemoryRouter
      initialEntries={['/clusters/team-a/alpha']}
      future={{ v7_startTransition: true, v7_relativeSplatPath: true }}
    >
      <Routes>
        <Route path="/clusters/:namespace/:name" element={<ClusterDetail />} />
      </Routes>
    </MemoryRouter>,
  );
}

const DETAIL_RESPONSE: ClusterDetailResponse = {
  cluster: {
    namespace: 'team-a',
    name: 'alpha',
    createdAt: '2026-03-24T12:00:00Z',
    phase: 'setting up primary',
    phaseReason: 'bootstrapping',
    desiredInstances: 3,
    readyInstances: 2,
    currentPrimary: 'alpha-1',
    image: 'ghcr.io/cloudnative-pg/postgresql:16.3',
    firstRecoverabilityPoint: '2026-03-24T10:00:00Z',
    lastSuccessfulBackup: '2026-03-24T11:45:00Z',
  },
  backups: [
    {
      namespace: 'team-a',
      name: 'alpha-backup',
      clusterName: 'alpha',
      phase: 'completed',
      method: 'barmanObjectStore',
      target: 'primary',
    },
  ],
  scheduledBackups: [
    {
      namespace: 'team-a',
      name: 'alpha-nightly',
      clusterName: 'alpha',
      schedule: '0 0 * * *',
      method: 'barmanObjectStore',
      target: 'primary',
      immediate: false,
      suspended: false,
      nextScheduleTime: '2026-03-25T00:00:00Z',
    },
  ],
};

function createMetricsResponse(alphaOneRatio: number): ClusterMetricsResponse {
  return {
    cluster: DETAIL_RESPONSE.cluster,
    overallHealth: alphaOneRatio > 0.9 ? 'critical' : 'warning',
    scrapedAt: '2026-03-24T12:05:00Z',
    scrapeError: 'alpha-2 scrape http://<redacted>:9187/metrics degraded',
    instances: [
      {
        podName: 'alpha-1',
        podStatus: 'healthy',
        health: alphaOneRatio > 0.9 ? 'critical' : 'warning',
        connections: {
          active: 20,
          idle: 60,
          idleInTransaction: 6,
          total: Math.round(alphaOneRatio * 100),
          maxConnections: 100,
        },
        replication: {
          replicationLagSeconds: 12.5,
          isReplica: false,
          isWalReceiverUp: true,
          streamingReplicas: 1,
          replayLagBytes: 0,
        },
        disk: {
          pvcCapacityBytes: 20 * 1024 * 1024 * 1024,
          databaseSizeBytes: 9 * 1024 * 1024 * 1024,
        },
        scrapedAt: '2026-03-24T12:05:00Z',
        scrapeError: '',
      },
      {
        podName: 'alpha-2',
        podStatus: 'failed',
        health: 'critical',
        connections: {
          active: 4,
          idle: 6,
          idleInTransaction: 0,
          total: 10,
          maxConnections: 100,
        },
        replication: {
          replicationLagSeconds: 45,
          isReplica: true,
          isWalReceiverUp: false,
          streamingReplicas: 0,
          replayLagBytes: 1024 * 1024 * 512,
        },
        disk: {
          pvcCapacityBytes: 10 * 1024 * 1024 * 1024,
          databaseSizeBytes: 9.5 * 1024 * 1024 * 1024,
        },
        scrapedAt: '2026-03-24T12:05:00Z',
        scrapeError: 'scrape http://<redacted>:9187/metrics: connection refused',
      },
    ],
  };
}

beforeEach(() => {
  MockWebSocket.instances = [];
  vi.stubGlobal('WebSocket', MockWebSocket as unknown as typeof WebSocket);
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('ClusterDetail', () => {
  test('renders detail data, threshold treatments, diagnostics, and refetches only for matching live updates', async () => {
    let metricsRequestCount = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url === '/api/clusters/team-a/alpha') {
        return createJSONResponse(DETAIL_RESPONSE);
      }
      if (url === '/api/clusters/team-a/alpha/metrics') {
        metricsRequestCount += 1;
        return createJSONResponse(createMetricsResponse(metricsRequestCount > 1 ? 0.95 : 0.86));
      }
      throw new Error(`Unexpected request: ${url}`);
    });

    vi.stubGlobal('fetch', fetchMock);

    renderDetailRoute();

    expect(screen.getByText('Loading cluster detail')).toBeInTheDocument();

    const clusterHeading = await screen.findByRole('heading', { name: 'alpha' });
    expect(clusterHeading).toBeInTheDocument();
    expect(screen.getByText('Pod Healthy')).toBeInTheDocument();
    expect(screen.getByText('Pod Failed')).toBeInTheDocument();
    expect(screen.getByText('scrape http://<redacted>:9187/metrics: connection refused')).toBeInTheDocument();

    const alphaOneConnections = screen.getByLabelText('alpha-1 connection saturation');
    const alphaTwoReplication = screen.getByLabelText('alpha-2 replication lag');
    const alphaTwoDisk = screen.getByLabelText('alpha-2 disk usage');

    expect(alphaOneConnections).toHaveAttribute('data-tone', 'warning');
    expect(alphaTwoReplication).toHaveAttribute('data-tone', 'critical');
    expect(alphaTwoDisk).toHaveAttribute('data-tone', 'critical');

    const alphaTwoCard = screen.getByRole('article', { name: 'Instance alpha-2' });
    expect(within(alphaTwoCard).getByText('Critical')).toBeInTheDocument();

    const [socket] = MockWebSocket.instances;
    socket.open();

    await waitFor(() => {
      expect(screen.getByText('Connected')).toBeInTheDocument();
    });

    socket.emit({
      type: 'store.changed',
      kind: 'Cluster',
      action: 'upsert',
      namespace: 'team-b',
      name: 'bravo',
      occurredAt: '2026-03-24T12:10:00Z',
    });

    await new Promise((resolve) => setTimeout(resolve, 200));
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(screen.getByText('Waiting for a matching cluster change')).toBeInTheDocument();

    socket.emit({
      type: 'store.changed',
      kind: 'Cluster',
      action: 'upsert',
      namespace: 'team-a',
      name: 'alpha',
      occurredAt: '2026-03-24T12:11:00Z',
    });

    await new Promise((resolve) => setTimeout(resolve, 200));

    await waitFor(() => {
      expect(screen.getByText('Refresh source')).toBeInTheDocument();
      expect(screen.getByText('live update')).toBeInTheDocument();
    });

    await waitFor(() => {
      expect(screen.getByLabelText('alpha-1 connection saturation')).toHaveAttribute('data-tone', 'critical');
    });

    expect(screen.getByText('upsert team-a/alpha')).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(4);
  });

  test('renders an explicit error state when either detail endpoint fails', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url === '/api/clusters/team-a/alpha') {
        return createJSONResponse(DETAIL_RESPONSE);
      }
      if (url === '/api/clusters/team-a/alpha/metrics') {
        return createJSONResponse({ error: 'metrics backend unavailable' }, 503);
      }
      throw new Error(`Unexpected request: ${url}`);
    });

    vi.stubGlobal('fetch', fetchMock);

    renderDetailRoute();

    expect(await screen.findByText('Could not load the cluster detail')).toBeInTheDocument();
    expect(screen.getByText('metrics backend unavailable')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Retry request' })).toBeInTheDocument();
  });
});
