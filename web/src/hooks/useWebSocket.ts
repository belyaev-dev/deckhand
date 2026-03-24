import { useEffect, useRef, useState } from 'react';
import type { WSChangeEvent, WebSocketStatus } from '../types/api';

const INITIAL_RECONNECT_DELAY_MS = 1000;
const DEFAULT_MAX_RECONNECT_DELAY_MS = 10000;

export interface UseWebSocketOptions {
  enabled?: boolean;
  onMessage?: (event: WSChangeEvent) => void;
  maxReconnectDelayMs?: number;
}

export interface UseWebSocketResult {
  status: WebSocketStatus;
  reconnectAttempt: number;
  reconnectDelayMs: number | null;
  lastMessageAt: number | null;
  lastMessage: WSChangeEvent | null;
  lastError: string | null;
}

export function buildWebSocketURL(path = '/ws') {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${protocol}//${window.location.host}${path}`;
}

function getReconnectDelay(attempt: number, maxReconnectDelayMs: number) {
  const exponentialDelay = INITIAL_RECONNECT_DELAY_MS * 2 ** Math.max(attempt - 1, 0);
  return Math.min(exponentialDelay, maxReconnectDelayMs);
}

export function useWebSocket({
  enabled = true,
  onMessage,
  maxReconnectDelayMs = DEFAULT_MAX_RECONNECT_DELAY_MS,
}: UseWebSocketOptions = {}): UseWebSocketResult {
  const onMessageRef = useRef(onMessage);
  const socketRef = useRef<WebSocket | null>(null);
  const reconnectTimerRef = useRef<number | null>(null);
  const reconnectAttemptRef = useRef(0);

  const [status, setStatus] = useState<WebSocketStatus>(enabled ? 'connecting' : 'disconnected');
  const [reconnectAttempt, setReconnectAttempt] = useState(0);
  const [reconnectDelayMs, setReconnectDelayMs] = useState<number | null>(null);
  const [lastMessageAt, setLastMessageAt] = useState<number | null>(null);
  const [lastMessage, setLastMessage] = useState<WSChangeEvent | null>(null);
  const [lastError, setLastError] = useState<string | null>(null);

  useEffect(() => {
    onMessageRef.current = onMessage;
  }, [onMessage]);

  useEffect(() => {
    if (!enabled) {
      setStatus('disconnected');
      return undefined;
    }

    let disposed = false;

    const clearReconnectTimer = () => {
      if (reconnectTimerRef.current !== null) {
        window.clearTimeout(reconnectTimerRef.current);
        reconnectTimerRef.current = null;
      }
    };

    const disconnectSocket = () => {
      const socket = socketRef.current;
      socketRef.current = null;
      if (!socket) {
        return;
      }

      socket.onopen = null;
      socket.onmessage = null;
      socket.onerror = null;
      socket.onclose = null;

      if (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING) {
        socket.close();
      }
    };

    const connect = () => {
      if (disposed) {
        return;
      }

      clearReconnectTimer();
      setStatus(reconnectAttemptRef.current > 0 ? 'reconnecting' : 'connecting');
      setReconnectDelayMs(null);

      let socket: WebSocket;
      try {
        socket = new WebSocket(buildWebSocketURL());
      } catch (error) {
        const message = error instanceof Error ? error.message : 'Unable to create a live updates socket.';
        setStatus('error');
        setLastError(message);
        return;
      }

      socketRef.current = socket;

      socket.onopen = () => {
        reconnectAttemptRef.current = 0;
        setReconnectAttempt(0);
        setReconnectDelayMs(null);
        setStatus('connected');
        setLastError(null);
      };

      socket.onmessage = (messageEvent) => {
        try {
          const parsed = JSON.parse(String(messageEvent.data)) as WSChangeEvent;
          setLastMessage(parsed);
          setLastMessageAt(Date.now());
          onMessageRef.current?.(parsed);
        } catch {
          setLastError('Received an invalid live updates payload.');
        }
      };

      socket.onerror = () => {
        setStatus((currentStatus) => (currentStatus === 'connected' ? 'error' : currentStatus));
        setLastError('Live updates connection error.');
      };

      socket.onclose = () => {
        socketRef.current = null;

        if (disposed) {
          setStatus('disconnected');
          return;
        }

        const nextAttempt = reconnectAttemptRef.current + 1;
        reconnectAttemptRef.current = nextAttempt;

        const nextDelay = getReconnectDelay(nextAttempt, maxReconnectDelayMs);
        setReconnectAttempt(nextAttempt);
        setReconnectDelayMs(nextDelay);
        setStatus('reconnecting');

        reconnectTimerRef.current = window.setTimeout(() => {
          connect();
        }, nextDelay);
      };
    };

    connect();

    return () => {
      disposed = true;
      clearReconnectTimer();
      disconnectSocket();
      setStatus('disconnected');
    };
  }, [enabled, maxReconnectDelayMs]);

  return {
    status,
    reconnectAttempt,
    reconnectDelayMs,
    lastMessageAt,
    lastMessage,
    lastError,
  };
}
