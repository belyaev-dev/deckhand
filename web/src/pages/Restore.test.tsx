import '@testing-library/jest-dom/vitest';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import Restore from './Restore';
import type { ClusterRestoreOptionsResponse, CreateRestoreResponse, RestoreStatusResponse } from '../types/api';

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

function buildRestoreOptionsResponse(): ClusterRestoreOptionsResponse {
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
      firstRecoverabilityPoint: '2026-03-24T10:00:00Z',
      lastSuccessfulBackup: '2026-03-24T11:30:00Z',
    },
    backups: [
      {
        namespace: 'team-a',
        name: 'alpha-backup-001',
        clusterName: 'alpha',
        createdAt: '2026-03-24T11:00:00Z',
        phase: 'completed',
        method: 'barmanObjectStore',
        target: 'primary',
        startedAt: '2026-03-24T11:00:10Z',
        stoppedAt: '2026-03-24T11:04:10Z',
        error: '',
      },
      {
        namespace: 'team-a',
        name: 'alpha-backup-000',
        clusterName: 'alpha',
        createdAt: '2026-03-24T10:00:00Z',
        phase: 'completed',
        method: 'volumeSnapshot',
        target: 'prefer-standby',
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
  };
}

function buildCreateRestoreResponse(): CreateRestoreResponse {
  return {
    sourceCluster: buildRestoreOptionsResponse().cluster,
    targetCluster: {
      namespace: 'team-b',
      name: 'alpha-restore',
      desiredInstances: 3,
      readyInstances: 0,
      phase: 'setting up primary',
      phaseReason: 'bootstrapping',
    },
    backup: buildRestoreOptionsResponse().backups[0],
    yamlPreview: [
      'apiVersion: postgresql.cnpg.io/v1',
      'kind: Cluster',
      'metadata:',
      '  name: alpha-restore',
      '  namespace: team-b',
      'spec:',
      '  bootstrap:',
      '    recovery:',
      '      source: alpha',
      '      recoveryTarget:',
      '        targetTime: "2026-03-24T11:15:00Z"',
    ].join('\n'),
    restoreStatus: {
      phase: 'bootstrapping',
      phaseReason: 'create accepted',
      message: 'restore cluster resource created',
      timestamps: {
        bootstrappingStartedAt: '2026-03-24T12:00:00Z',
        lastTransitionAt: '2026-03-24T12:00:00Z',
      },
    },
  };
}

function buildRestoreStatusResponse(overrides: Partial<RestoreStatusResponse> = {}): RestoreStatusResponse {
  return {
    cluster: {
      namespace: 'team-b',
      name: 'alpha-restore',
      desiredInstances: 3,
      readyInstances: 1,
      phase: 'restoring',
      phaseReason: 'recovering',
      currentPrimary: 'alpha-restore-1',
    },
    status: {
      phase: 'recovering',
      phaseReason: 'recovering',
      message: 'cloudnative-pg is applying recovery work',
      error: '',
      timestamps: {
        bootstrappingStartedAt: '2026-03-24T12:00:00Z',
        recoveringStartedAt: '2026-03-24T12:02:00Z',
        lastTransitionAt: '2026-03-24T12:02:00Z',
      },
    },
    ...overrides,
  };
}

function renderRestoreRoute(initialEntry = '/clusters/team-a/alpha/restore?backup=alpha-backup-001') {
  return render(
    <MemoryRouter
      initialEntries={[initialEntry]}
      future={{ v7_startTransition: true, v7_relativeSplatPath: true }}
    >
      <Routes>
        <Route path="/clusters/:namespace/:name/restore" element={<Restore />} />
      </Routes>
    </MemoryRouter>,
  );
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

describe('Restore', () => {
  test('guides the operator through restore creation, shows backend YAML, and refetches status only for matching target-cluster events', async () => {
    const user = userEvent.setup();
    let restoreOptionsRequests = 0;
    let restoreStatusRequests = 0;
    let submittedRequestBody = '';

    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);

      if (url === '/api/clusters/team-a/alpha/restore' && (!init?.method || init.method === 'GET')) {
        restoreOptionsRequests += 1;
        return createJSONResponse(buildRestoreOptionsResponse());
      }

      if (url === '/api/clusters/team-a/alpha/restore' && init?.method === 'POST') {
        submittedRequestBody = String(init.body);
        return createJSONResponse(buildCreateRestoreResponse(), 201);
      }

      if (url === '/api/clusters/team-b/alpha-restore/restore-status') {
        restoreStatusRequests += 1;
        if (restoreStatusRequests === 1) {
          return createJSONResponse(buildRestoreStatusResponse());
        }

        return createJSONResponse(
          buildRestoreStatusResponse({
            cluster: {
              namespace: 'team-b',
              name: 'alpha-restore',
              desiredInstances: 3,
              readyInstances: 3,
              phase: 'healthy',
              phaseReason: 'ready',
              currentPrimary: 'alpha-restore-1',
            },
            status: {
              phase: 'ready',
              phaseReason: 'ready',
              message: 'cluster is ready',
              error: '',
              timestamps: {
                bootstrappingStartedAt: '2026-03-24T12:00:00Z',
                recoveringStartedAt: '2026-03-24T12:02:00Z',
                readyAt: '2026-03-24T12:10:00Z',
                lastTransitionAt: '2026-03-24T12:10:00Z',
              },
            },
          }),
        );
      }

      throw new Error(`Unexpected request: ${url}`);
    });

    vi.stubGlobal('fetch', fetchMock);

    renderRestoreRoute();

    expect(await screen.findByRole('heading', { name: 'Restore alpha into a new cluster' })).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.getByDisplayValue('alpha-backup-001')).toBeChecked();
    });
    expect(screen.getByText('Creates a new cluster')).toBeInTheDocument();

    const [socket] = MockWebSocket.instances;
    socket.open();

    await waitFor(() => {
      expect(screen.getByText('Connected')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Continue to restore settings' }));

    const targetNamespaceInput = screen.getByRole('textbox', { name: /target namespace/i });
    await user.clear(targetNamespaceInput);
    await user.type(targetNamespaceInput, 'team-b');
    await user.clear(screen.getByLabelText('PITR target time'));
    await user.type(screen.getByLabelText('PITR target time'), '2026-03-24T09:59:00Z');

    expect(screen.getByText('PITR target time must stay within 2026-03-24T10:00:00Z to 2026-03-24T11:30:00Z.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Continue to confirmation' })).toBeDisabled();

    await user.clear(screen.getByLabelText('PITR target time'));
    await user.type(screen.getByLabelText('PITR target time'), '2026-03-24T11:15:00Z');

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Continue to confirmation' })).toBeEnabled();
    });

    await user.click(screen.getByRole('button', { name: 'Continue to confirmation' }));

    expect(await screen.findByRole('heading', { name: 'Confirm the restore request' })).toBeInTheDocument();
    expect(screen.getAllByText('team-b/alpha-restore').length).toBeGreaterThan(0);
    expect(screen.getByText('alpha-backup-001')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Create restore cluster' }));

    expect(await screen.findByRole('heading', { name: 'Monitor the target cluster' })).toBeInTheDocument();
    expect(submittedRequestBody).toContain('"backupName":"alpha-backup-001"');
    expect(submittedRequestBody).toContain('"targetNamespace":"team-b"');
    expect(submittedRequestBody).toContain('"targetName":"alpha-restore"');
    expect(submittedRequestBody).toContain('"pitrTargetTime":"2026-03-24T11:15:00Z"');

    await user.click(screen.getByRole('button', { name: 'Show YAML' }));

    expect(
      await screen.findByText((content, element) => element?.tagName === 'PRE' && content.includes('kind: Cluster')),
    ).toBeInTheDocument();
    expect(
      screen.getByText((content, element) => element?.tagName === 'PRE' && content.includes('targetTime: "2026-03-24T11:15:00Z"')),
    ).toBeInTheDocument();

    await waitFor(() => {
      expect(restoreStatusRequests).toBe(1);
    });
    expect(screen.getByText('cloudnative-pg is applying recovery work')).toBeInTheDocument();

    socket.emit({
      type: 'store.changed',
      kind: 'backup',
      action: 'upsert',
      namespace: 'team-a',
      name: 'alpha-backup-001',
      occurredAt: '2026-03-24T12:03:00Z',
    });

    await new Promise((resolve) => setTimeout(resolve, 200));
    expect(restoreStatusRequests).toBe(1);

    socket.emit({
      type: 'store.changed',
      kind: 'cluster',
      action: 'upsert',
      namespace: 'team-b',
      name: 'alpha-restore',
      occurredAt: '2026-03-24T12:10:00Z',
    });

    await waitFor(() => {
      expect(restoreStatusRequests).toBe(2);
    });

    await waitFor(() => {
      expect(screen.getByText('cluster is ready')).toBeInTheDocument();
    });
    expect(screen.getByText('cluster upsert team-b/alpha-restore')).toBeInTheDocument();
    expect(restoreOptionsRequests).toBe(1);
  });

  test('surfaces restore creation failures without clearing the loaded backup context', async () => {
    const user = userEvent.setup();

    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url === '/api/clusters/team-a/alpha/restore' && (!init?.method || init.method === 'GET')) {
        return createJSONResponse(buildRestoreOptionsResponse());
      }
      if (url === '/api/clusters/team-a/alpha/restore' && init?.method === 'POST') {
        return createJSONResponse({ error: 'cluster "alpha-restore" in namespace "team-b" already exists' }, 409);
      }
      throw new Error(`Unexpected request: ${url}`);
    });

    vi.stubGlobal('fetch', fetchMock);

    renderRestoreRoute();

    expect(await screen.findByRole('heading', { name: 'Restore alpha into a new cluster' })).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Continue to restore settings' }));
    const targetNamespaceInput = screen.getByRole('textbox', { name: /target namespace/i });
    await user.clear(targetNamespaceInput);
    await user.type(targetNamespaceInput, 'team-b');
    await user.click(screen.getByRole('button', { name: 'Continue to confirmation' }));
    await user.click(screen.getByRole('button', { name: 'Create restore cluster' }));

    expect(await screen.findByText('cluster "alpha-restore" in namespace "team-b" already exists')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Restore alpha into a new cluster' })).toBeInTheDocument();
    expect(screen.getByText('alpha-backup-001')).toBeInTheDocument();
  });
});
