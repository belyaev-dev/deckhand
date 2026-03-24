import '@testing-library/jest-dom/vitest';
import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import Backups from './Backups';
import type { ClusterBackupsResponse, CreateBackupResponse } from '../types/api';

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

function renderBackupsRoute() {
  return render(
    <MemoryRouter
      initialEntries={['/clusters/team-a/alpha/backups']}
      future={{ v7_startTransition: true, v7_relativeSplatPath: true }}
    >
      <Routes>
        <Route path="/clusters/:namespace/:name/backups" element={<Backups />} />
      </Routes>
    </MemoryRouter>,
  );
}

function buildBackupsResponse(overrides: Partial<ClusterBackupsResponse> = {}): ClusterBackupsResponse {
  return {
    cluster: {
      namespace: 'team-a',
      name: 'alpha',
      createdAt: '2026-03-24T08:00:00Z',
      phase: 'healthy',
      phaseReason: 'ready',
      desiredInstances: 3,
      readyInstances: 3,
      currentPrimary: 'alpha-1',
      image: 'ghcr.io/cloudnative-pg/postgresql:16.3',
      firstRecoverabilityPoint: '2026-03-24T09:00:00Z',
      lastSuccessfulBackup: '2026-03-24T10:00:00Z',
    },
    backups: [
      {
        namespace: 'team-a',
        name: 'alpha-backup-001',
        clusterName: 'alpha',
        createdAt: '2026-03-24T09:55:00Z',
        phase: 'completed',
        method: 'barmanObjectStore',
        target: 'primary',
        startedAt: '2026-03-24T09:55:10Z',
        stoppedAt: '2026-03-24T09:58:20Z',
        error: '',
      },
    ],
    scheduledBackups: [
      {
        namespace: 'team-a',
        name: 'alpha-nightly',
        clusterName: 'alpha',
        createdAt: '2026-03-23T23:59:00Z',
        schedule: '0 0 * * *',
        method: 'barmanObjectStore',
        target: 'primary',
        immediate: false,
        suspended: false,
        lastScheduleTime: '2026-03-24T00:00:00Z',
        nextScheduleTime: '2026-03-25T00:00:00Z',
      },
    ],
    ...overrides,
  };
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

describe('Backups', () => {
  test('renders schedule and history data, triggers a backup, and refetches when a matching backup event arrives', async () => {
    const user = userEvent.setup();
    let listRequestCount = 0;
    let resolveCreate: ((response: Response) => void) | null = null;

    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);

      if (url === '/api/clusters/team-a/alpha/backups' && (!init?.method || init.method === 'GET')) {
        listRequestCount += 1;

        if (listRequestCount === 1) {
          return Promise.resolve(createJSONResponse(buildBackupsResponse()));
        }

        if (listRequestCount === 2) {
          return Promise.resolve(
            createJSONResponse(
              buildBackupsResponse({
                backups: [
                  {
                    namespace: 'team-a',
                    name: 'alpha-manual-002',
                    clusterName: 'alpha',
                    createdAt: '2026-03-24T12:00:10Z',
                    phase: 'running',
                    method: 'barmanObjectStore',
                    target: 'primary',
                    startedAt: '2026-03-24T12:00:12Z',
                    stoppedAt: null,
                    error: '',
                  },
                  ...buildBackupsResponse().backups,
                ],
              }),
            ),
          );
        }

        return Promise.resolve(
          createJSONResponse(
            buildBackupsResponse({
              backups: [
                {
                  namespace: 'team-a',
                  name: 'alpha-manual-002',
                  clusterName: 'alpha',
                  createdAt: '2026-03-24T12:00:10Z',
                  phase: 'completed',
                  method: 'barmanObjectStore',
                  target: 'primary',
                  startedAt: '2026-03-24T12:00:12Z',
                  stoppedAt: '2026-03-24T12:02:00Z',
                  error: '',
                },
                ...buildBackupsResponse().backups,
              ],
            }),
          ),
        );
      }

      if (url === '/api/clusters/team-a/alpha/backups' && init?.method === 'POST') {
        return new Promise<Response>((resolve) => {
          resolveCreate = resolve;
        });
      }

      return Promise.reject(new Error(`Unexpected request: ${url}`));
    });

    vi.stubGlobal('fetch', fetchMock);

    renderBackupsRoute();

    expect(screen.getByText('Loading backup management')).toBeInTheDocument();

    expect(await screen.findByRole('heading', { name: 'Backups for alpha' })).toBeInTheDocument();
    expect(screen.getByText('2h ago')).toBeInTheDocument();
    expect(screen.getByText('Fresh', { selector: '.backup-pill' })).toBeInTheDocument();
    expect(screen.getByText('alpha-nightly')).toBeInTheDocument();
    expect(screen.getByText('0 0 * * *')).toBeInTheDocument();
    expect(screen.getByRole('rowheader', { name: 'alpha-backup-001' })).toBeInTheDocument();

    const [socket] = MockWebSocket.instances;
    socket.open();

    await waitFor(() => {
      expect(screen.getByText('Connected')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Backup now' }));

    expect(screen.getByRole('button', { name: 'Creating backup…' })).toBeDisabled();

    const createResponse: CreateBackupResponse = {
      backup: {
        namespace: 'team-a',
        name: 'alpha-manual-002',
        clusterName: 'alpha',
        createdAt: '2026-03-24T12:00:10Z',
        phase: 'running',
        method: 'barmanObjectStore',
        target: 'primary',
        startedAt: '2026-03-24T12:00:12Z',
        stoppedAt: null,
        error: '',
      },
    };
    expect(resolveCreate).not.toBeNull();
    resolveCreate!(createJSONResponse(createResponse, 201));

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Backup now' })).toBeEnabled();
    });

    const runningRowHeader = await screen.findByRole('rowheader', { name: 'alpha-manual-002' });
    const runningRow = runningRowHeader.closest('tr');
    expect(runningRow).not.toBeNull();
    expect(within(runningRow as HTMLTableRowElement).getByText('Running')).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith('/api/clusters/team-a/alpha/backups', expect.objectContaining({ method: 'POST' }));

    socket.emit({
      type: 'store.changed',
      kind: 'backup',
      action: 'upsert',
      namespace: 'team-b',
      name: 'bravo-backup-001',
      occurredAt: '2026-03-24T12:03:00Z',
    });

    await new Promise((resolve) => setTimeout(resolve, 200));
    expect(listRequestCount).toBe(2);

    socket.emit({
      type: 'store.changed',
      kind: 'backup',
      action: 'upsert',
      namespace: 'team-a',
      name: 'alpha-manual-002',
      occurredAt: '2026-03-24T12:04:00Z',
    });

    await new Promise((resolve) => setTimeout(resolve, 200));

    await waitFor(() => {
      expect(screen.getByText('backup upsert team-a/alpha-manual-002')).toBeInTheDocument();
    });

    const completedRowHeader = screen.getByRole('rowheader', { name: 'alpha-manual-002' });
    const completedRow = completedRowHeader.closest('tr');
    expect(completedRow).not.toBeNull();
    expect(within(completedRow as HTMLTableRowElement).getByText('Completed')).toBeInTheDocument();
    expect(within(completedRow as HTMLTableRowElement).getByText('1m 48s')).toBeInTheDocument();
    expect(listRequestCount).toBe(3);
    expect(screen.getByText('live update')).toBeInTheDocument();
  });

  test('surfaces create failures without clearing existing schedule or history data', async () => {
    const user = userEvent.setup();
    const response = buildBackupsResponse({
      backups: [
        {
          namespace: 'team-a',
          name: 'alpha-backup-failed',
          clusterName: 'alpha',
          createdAt: '2026-03-24T08:30:00Z',
          phase: 'failed',
          method: 'barmanObjectStore',
          target: 'primary',
          startedAt: '2026-03-24T08:30:10Z',
          stoppedAt: '2026-03-24T08:31:10Z',
          error: 'object store credentials rejected the upload',
        },
      ],
    });

    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url === '/api/clusters/team-a/alpha/backups' && (!init?.method || init.method === 'GET')) {
        return createJSONResponse(response);
      }
      if (url === '/api/clusters/team-a/alpha/backups' && init?.method === 'POST') {
        return createJSONResponse({ error: 'create backup for cluster "alpha" in namespace "team-a": backup already exists' }, 409);
      }
      throw new Error(`Unexpected request: ${url}`);
    });

    vi.stubGlobal('fetch', fetchMock);

    renderBackupsRoute();

    expect(await screen.findByRole('heading', { name: 'Backups for alpha' })).toBeInTheDocument();
    expect(screen.getByRole('rowheader', { name: 'alpha-backup-failed' })).toBeInTheDocument();
    expect(screen.getByText('object store credentials rejected the upload')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Backup now' }));

    expect(await screen.findByText('create backup for cluster "alpha" in namespace "team-a": backup already exists')).toBeInTheDocument();
    expect(screen.getByRole('rowheader', { name: 'alpha-backup-failed' })).toBeInTheDocument();
    expect(screen.getByText('alpha-nightly')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Backup now' })).toBeEnabled();
  });
});
