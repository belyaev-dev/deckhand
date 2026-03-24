import '@testing-library/jest-dom/vitest';
import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';
import { MemoryRouter } from 'react-router-dom';
import Overview from './Overview';
import type { ClusterListResponse } from '../types/api';

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

function createOverviewResponse(items: ClusterListResponse['items']): ClusterListResponse {
  const namespaceCounts = items.reduce<Record<string, number>>((counts, item) => {
    counts[item.namespace] = (counts[item.namespace] ?? 0) + 1;
    return counts;
  }, {});

  return {
    items,
    namespaces: Object.entries(namespaceCounts)
      .sort(([left], [right]) => left.localeCompare(right))
      .map(([name, clusterCount]) => ({ name, clusterCount })),
  };
}

function renderOverview() {
  return render(
    <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
      <Overview />
    </MemoryRouter>,
  );
}

const ALL_CLUSTERS = createOverviewResponse([
  {
    namespace: 'team-a',
    name: 'alpha',
    phase: 'setting up primary',
    phaseReason: 'bootstrapping',
    desiredInstances: 3,
    readyInstances: 2,
    currentPrimary: 'alpha-1',
    overallHealth: 'warning',
    lastSuccessfulBackup: '2026-03-24T11:30:00Z',
    metricsScrapeError: '',
    metricsScrapedAt: '2026-03-24T11:50:00Z',
  },
  {
    namespace: 'team-b',
    name: 'bravo',
    phase: 'healthy',
    phaseReason: 'ready',
    desiredInstances: 2,
    readyInstances: 2,
    currentPrimary: 'bravo-1',
    overallHealth: 'healthy',
    lastSuccessfulBackup: '2026-03-24T11:45:00Z',
    metricsScrapeError: '',
    metricsScrapedAt: '2026-03-24T11:55:00Z',
  },
]);

const TEAM_A_ONLY = createOverviewResponse([ALL_CLUSTERS.items[0]]);

beforeEach(() => {
  MockWebSocket.instances = [];
  vi.stubGlobal('WebSocket', MockWebSocket as unknown as typeof WebSocket);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('Overview', () => {
  test('renders live cluster rows, exposes detail links, and filters by namespace through the API contract', async () => {
    const user = userEvent.setup();
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('namespace=team-a')) {
        return createJSONResponse(TEAM_A_ONLY);
      }
      return createJSONResponse(ALL_CLUSTERS);
    });

    vi.stubGlobal('fetch', fetchMock);

    renderOverview();

    expect(screen.getByText('Loading cluster overview')).toBeInTheDocument();

    const alphaCard = await screen.findByRole('article', { name: 'team-a/alpha' });
    const alphaLink = screen.getByRole('link', { name: 'Open detail dashboard for team-a/alpha' });

    expect(alphaCard).toBeInTheDocument();
    expect(alphaLink).toHaveAttribute('href', '/clusters/team-a/alpha');
    expect(screen.getByRole('link', { name: 'Open detail dashboard for team-b/bravo' })).toHaveAttribute(
      'href',
      '/clusters/team-b/bravo',
    );
    expect(screen.getByRole('article', { name: 'team-b/bravo' })).toBeInTheDocument();
    expect(within(alphaCard).getByText('Warning')).toBeInTheDocument();
    expect(within(alphaCard).getByText('setting up primary')).toBeInTheDocument();
    expect(within(alphaCard).getByText('Inspect live health details')).toBeInTheDocument();

    await user.selectOptions(screen.getByRole('combobox', { name: 'Namespace' }), 'team-a');

    await waitFor(() => {
      expect(screen.queryByRole('article', { name: 'team-b/bravo' })).not.toBeInTheDocument();
    });

    expect(screen.getByRole('article', { name: 'team-a/alpha' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Open detail dashboard for team-a/alpha' })).toHaveAttribute(
      'href',
      '/clusters/team-a/alpha',
    );
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/clusters?namespace=team-a',
      expect.objectContaining({ headers: { Accept: 'application/json' } }),
    );
  });

  test('filters by health without another API round-trip', async () => {
    const user = userEvent.setup();
    const fetchMock = vi.fn(async () => createJSONResponse(ALL_CLUSTERS));

    vi.stubGlobal('fetch', fetchMock);

    renderOverview();

    await screen.findByRole('article', { name: 'team-a/alpha' });

    await user.selectOptions(screen.getByRole('combobox', { name: 'Health' }), 'healthy');

    await waitFor(() => {
      expect(screen.queryByRole('article', { name: 'team-a/alpha' })).not.toBeInTheDocument();
    });

    expect(screen.getByRole('article', { name: 'team-b/bravo' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Open detail dashboard for team-b/bravo' })).toHaveAttribute(
      'href',
      '/clusters/team-b/bravo',
    );
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  test('shows empty and error states explicitly', async () => {
    const fetchMock = vi.fn();
    fetchMock
      .mockResolvedValueOnce(createJSONResponse({ items: [], namespaces: [] }))
      .mockResolvedValueOnce(createJSONResponse({ error: 'backend unavailable' }, 503));

    vi.stubGlobal('fetch', fetchMock);

    const firstRender = renderOverview();
    expect(await screen.findByText('No clusters discovered yet')).toBeInTheDocument();

    firstRender.unmount();

    renderOverview();
    expect(await screen.findByText('Could not load the cluster overview')).toBeInTheDocument();
    expect(screen.getByText('backend unavailable')).toBeInTheDocument();
  });

  test('refetches when the websocket announces a store change', async () => {
    const fetchMock = vi.fn();
    fetchMock
      .mockResolvedValueOnce(createJSONResponse(ALL_CLUSTERS))
      .mockResolvedValueOnce(
        createJSONResponse(
          createOverviewResponse([
            {
              ...ALL_CLUSTERS.items[0],
              overallHealth: 'critical',
              readyInstances: 1,
            },
          ]),
        ),
      );

    vi.stubGlobal('fetch', fetchMock);

    renderOverview();

    const [socket] = MockWebSocket.instances;

    await screen.findByRole('article', { name: 'team-a/alpha' });
    socket.open();

    await waitFor(() => {
      expect(screen.getByText('Connected')).toBeInTheDocument();
    });

    socket.emit({
      type: 'store.changed',
      kind: 'Cluster',
      action: 'upsert',
      namespace: 'team-a',
      name: 'alpha',
      occurredAt: '2026-03-24T12:00:00Z',
    });

    await waitFor(() => {
      const clusterCard = screen.getByRole('article', { name: 'team-a/alpha' });
      expect(within(clusterCard).getByText('Critical')).toBeInTheDocument();
    });

    expect(screen.getByText('upsert team-a/alpha')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Open detail dashboard for team-a/alpha' })).toHaveAttribute(
      'href',
      '/clusters/team-a/alpha',
    );
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });
});
