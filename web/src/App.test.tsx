import '@testing-library/jest-dom/vitest';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';
import { MemoryRouter } from 'react-router-dom';
import App from './App';

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

beforeEach(() => {
  MockWebSocket.instances = [];
  vi.stubGlobal('WebSocket', MockWebSocket as unknown as typeof WebSocket);
  vi.spyOn(Date, 'now').mockReturnValue(new Date('2026-03-24T12:00:00Z').valueOf());
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('App', () => {
  test('renders the routed shell, navigates from overview to detail to backups to restore, and returns to overview', async () => {
    const user = userEvent.setup();
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url === '/api/clusters') {
        return createJSONResponse({
          namespaces: [{ name: 'team-a', clusterCount: 1 }],
          items: [
            {
              namespace: 'team-a',
              name: 'alpha',
              phase: 'healthy',
              phaseReason: 'ready',
              desiredInstances: 3,
              readyInstances: 3,
              currentPrimary: 'alpha-1',
              lastSuccessfulBackup: '2026-03-24T10:00:00Z',
              overallHealth: 'healthy',
              metricsScrapeError: '',
              metricsScrapedAt: '2026-03-24T11:55:00Z',
            },
          ],
        });
      }
      if (url === '/api/clusters/team-a/alpha') {
        return createJSONResponse({
          cluster: {
            namespace: 'team-a',
            name: 'alpha',
            desiredInstances: 3,
            readyInstances: 2,
            phase: 'setting up primary',
            phaseReason: 'bootstrapping',
            currentPrimary: 'alpha-1',
            createdAt: '2026-03-24T12:00:00Z',
            image: 'ghcr.io/cloudnative-pg/postgresql:16.3',
            firstRecoverabilityPoint: '2026-03-24T10:00:00Z',
            lastSuccessfulBackup: '2026-03-24T11:45:00Z',
          },
          backups: [],
          scheduledBackups: [],
        });
      }
      if (url === '/api/clusters/team-a/alpha/metrics') {
        return createJSONResponse({
          cluster: {
            namespace: 'team-a',
            name: 'alpha',
            desiredInstances: 3,
            readyInstances: 2,
          },
          overallHealth: 'warning',
          scrapedAt: '2026-03-24T12:05:00Z',
          scrapeError: 'metrics not available yet',
          instances: [
            {
              podName: 'alpha-1',
              podStatus: 'healthy',
              health: 'warning',
              connections: {
                active: 20,
                idle: 50,
                idleInTransaction: 4,
                total: 74,
                maxConnections: 100,
              },
              replication: {
                replicationLagSeconds: 12,
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
          ],
        });
      }
      if (url === '/api/clusters/team-a/alpha/backups') {
        return createJSONResponse({
          cluster: {
            namespace: 'team-a',
            name: 'alpha',
            desiredInstances: 3,
            readyInstances: 2,
            phase: 'healthy',
            phaseReason: 'ready',
            currentPrimary: 'alpha-1',
            createdAt: '2026-03-24T12:00:00Z',
            lastSuccessfulBackup: '2026-03-24T10:00:00Z',
          },
          backups: [
            {
              namespace: 'team-a',
              name: 'alpha-backup-001',
              clusterName: 'alpha',
              createdAt: '2026-03-24T10:00:00Z',
              phase: 'completed',
              method: 'barmanObjectStore',
              target: 'primary',
              startedAt: '2026-03-24T10:00:10Z',
              stoppedAt: '2026-03-24T10:03:10Z',
              error: '',
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
              lastScheduleTime: '2026-03-24T00:00:00Z',
              nextScheduleTime: '2026-03-25T00:00:00Z',
            },
          ],
        });
      }
      if (url === '/api/clusters/team-a/alpha/restore') {
        return createJSONResponse({
          cluster: {
            namespace: 'team-a',
            name: 'alpha',
            desiredInstances: 3,
            readyInstances: 2,
            phase: 'healthy',
            phaseReason: 'ready',
            currentPrimary: 'alpha-1',
            firstRecoverabilityPoint: '2026-03-24T10:00:00Z',
            lastSuccessfulBackup: '2026-03-24T11:30:00Z',
          },
          backups: [
            {
              namespace: 'team-a',
              name: 'alpha-backup-001',
              clusterName: 'alpha',
              createdAt: '2026-03-24T10:00:00Z',
              phase: 'completed',
              method: 'barmanObjectStore',
              target: 'primary',
              startedAt: '2026-03-24T10:00:10Z',
              stoppedAt: '2026-03-24T10:03:10Z',
              error: '',
            },
          ],
          recoverability: {
            start: '2026-03-24T10:00:00Z',
            end: '2026-03-24T11:30:00Z',
          },
          supportedPhases: ['bootstrapping', 'recovering', 'ready', 'failed'],
        });
      }
      throw new Error(`Unexpected request: ${url}`);
    });

    vi.stubGlobal('fetch', fetchMock);

    render(
      <MemoryRouter
        initialEntries={['/']}
        future={{ v7_startTransition: true, v7_relativeSplatPath: true }}
      >
        <App />
      </MemoryRouter>,
    );

    expect(screen.getByRole('heading', { name: 'Deckhand overview' })).toBeInTheDocument();
    expect(screen.getByRole('navigation', { name: 'Primary' })).toBeInTheDocument();

    await waitFor(() => {
      expect(screen.getByRole('heading', { name: 'Cluster overview' })).toBeInTheDocument();
    });

    const overviewEntry = screen.getByRole('link', { name: 'Open detail dashboard for team-a/alpha' });
    expect(overviewEntry).toHaveAttribute('href', '/clusters/team-a/alpha');
    expect(screen.getByRole('article', { name: 'team-a/alpha' })).toBeInTheDocument();

    MockWebSocket.instances[0]?.open();

    await waitFor(() => {
      expect(screen.getByText('Connected')).toBeInTheDocument();
    });

    await user.click(overviewEntry);

    expect(await screen.findByRole('heading', { name: 'alpha' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Overview' })).toHaveAttribute('href', '/');
    expect(screen.getByRole('link', { name: 'team-a/alpha' })).toHaveClass('app-shell__nav-link--active');
    expect(screen.getByRole('link', { name: 'Manage backups' })).toHaveAttribute('href', '/clusters/team-a/alpha/backups');
    expect(screen.getByText('Pod Healthy')).toBeInTheDocument();

    await user.click(screen.getByRole('link', { name: 'Manage backups' }));

    expect(await screen.findByRole('heading', { name: 'Backups for alpha' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'team-a/alpha' })).toHaveAttribute('href', '/clusters/team-a/alpha');
    expect(screen.getByRole('link', { name: 'Backups' })).toHaveClass('app-shell__nav-link--active');
    expect(screen.getByRole('link', { name: 'Back to cluster detail' })).toHaveAttribute('href', '/clusters/team-a/alpha');
    expect(screen.getByText('alpha-nightly')).toBeInTheDocument();

    await user.click(screen.getByRole('link', { name: 'Restore from backup' }));

    expect(await screen.findByRole('heading', { name: 'Restore alpha into a new cluster' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Restore' })).toHaveClass('app-shell__nav-link--active');
    expect(screen.getByRole('link', { name: 'Back to backups' })).toHaveAttribute('href', '/clusters/team-a/alpha/backups');
    expect(screen.getByRole('radio', { name: /alpha-backup-001/i })).toBeChecked();

    await user.click(screen.getByRole('link', { name: 'Back to backups' }));

    expect(await screen.findByRole('heading', { name: 'Backups for alpha' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Backups' })).toHaveClass('app-shell__nav-link--active');

    await user.click(screen.getByRole('link', { name: 'Back to cluster detail' }));

    expect(await screen.findByRole('heading', { name: 'alpha' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'team-a/alpha' })).toHaveClass('app-shell__nav-link--active');

    await user.click(screen.getByRole('link', { name: 'Back to overview' }));

    expect(await screen.findByRole('heading', { name: 'Cluster overview' })).toBeInTheDocument();
    expect(screen.getByRole('article', { name: 'team-a/alpha' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Overview' })).toHaveClass('app-shell__nav-link--active');
  });

  test('registers the restore route in the app shell on direct entry', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url === '/api/clusters/team-a/alpha/restore') {
        return createJSONResponse({
          cluster: {
            namespace: 'team-a',
            name: 'alpha',
            desiredInstances: 3,
            readyInstances: 2,
            phase: 'healthy',
            phaseReason: 'ready',
            currentPrimary: 'alpha-1',
            firstRecoverabilityPoint: '2026-03-24T10:00:00Z',
            lastSuccessfulBackup: '2026-03-24T11:30:00Z',
          },
          backups: [
            {
              namespace: 'team-a',
              name: 'alpha-backup-001',
              clusterName: 'alpha',
              createdAt: '2026-03-24T10:00:00Z',
              phase: 'completed',
              method: 'barmanObjectStore',
              target: 'primary',
              startedAt: '2026-03-24T10:00:10Z',
              stoppedAt: '2026-03-24T10:03:10Z',
              error: '',
            },
          ],
          recoverability: {
            start: '2026-03-24T10:00:00Z',
            end: '2026-03-24T11:30:00Z',
          },
          supportedPhases: ['bootstrapping', 'recovering', 'ready', 'failed'],
        });
      }
      throw new Error(`Unexpected request: ${url}`);
    });

    vi.stubGlobal('fetch', fetchMock);

    render(
      <MemoryRouter
        initialEntries={['/clusters/team-a/alpha/restore?backup=alpha-backup-001']}
        future={{ v7_startTransition: true, v7_relativeSplatPath: true }}
      >
        <App />
      </MemoryRouter>,
    );

    expect(await screen.findByRole('heading', { name: 'Restore alpha into a new cluster' })).toBeInTheDocument();
    expect(screen.getAllByRole('link', { name: 'Overview' }).length).toBeGreaterThan(0);
    expect(screen.getByRole('link', { name: 'team-a/alpha' })).toHaveAttribute('href', '/clusters/team-a/alpha');
    expect(screen.getByRole('link', { name: 'Backups' })).toHaveAttribute('href', '/clusters/team-a/alpha/backups');
    expect(screen.getByRole('link', { name: 'Restore' })).toHaveClass('app-shell__nav-link--active');
    expect(screen.getByRole('link', { name: 'Back to backups' })).toHaveAttribute('href', '/clusters/team-a/alpha/backups');
  });
});
