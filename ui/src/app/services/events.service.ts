import { Injectable, signal, effect, OnDestroy } from '@angular/core';
import { Observable, Subject } from 'rxjs';

import { ConfigService } from './config.service';
import { RunEvent } from '../models/run.model';

@Injectable({
  providedIn: 'root'
})
export class EventsService implements OnDestroy {
  // Signals for state
  private readonly _socket = signal<WebSocket | null>(null);
  private readonly _events = signal<RunEvent[]>([]);
  private readonly _connected = signal<boolean>(false);
  private readonly _error = signal<string | null>(null);
  private readonly _reconnectAttempts = signal<number>(0);
  
  private readonly maxReconnectAttempts = 5;
  private readonly reconnectDelay = 3000; // 3 seconds
  
  private destroy$ = new Subject<void>();

  // Public readonly signals
  readonly socket = this._socket.asReadonly();
  readonly events = this._events.asReadonly();
  readonly connected = this._connected.asReadonly();
  readonly error = this._error.asReadonly();
  readonly reconnectAttempts = this._reconnectAttempts.asReadonly();

  constructor(private config: ConfigService) {
    // Effect to update connection status
    effect(() => {
      const sock = this._socket();
      if (sock) {
        this._connected.set(sock.readyState === WebSocket.OPEN);
      } else {
        this._connected.set(false);
      }
    });
  }

  /**
   * Connect to the WebSocket stream for a specific run
   */
  connectToRun(runRef: string): void {
    this.disconnect();
    
    const wsUrl = `${this.config.wsUrl()}/runs/${runRef}/stream`;
    
    const socket = new WebSocket(wsUrl);
    this._socket.set(socket);
    this._reconnectAttempts.set(0);

    socket.onopen = () => {
      this._reconnectAttempts.set(0);
      this._connected.set(true);
      this._error.set(null);
    };

    socket.onmessage = (event) => {
      try {
        const data = JSON.parse(event.data);
        this.handleMessage(data);
      } catch (error) {
        this._error.set('Failed to parse message');
      }
    };

    socket.onerror = (error) => {
      this._error.set('WebSocket error');
    };

    socket.onclose = () => {
      this._connected.set(false);
      // Attempt to reconnect
      if (this._reconnectAttempts() < this.maxReconnectAttempts) {
        setTimeout(() => {
          this._reconnectAttempts.update(attempts => attempts + 1);
          this.connectToRun(runRef);
        }, this.reconnectDelay);
      }
    };
  }

  /**
   * Handle incoming WebSocket message
   */
  private handleMessage(data: any): void {
    if (data.type === 'event' && data.data) {
      const runEvent: RunEvent = {
        id: data.data.id || 0,
        runId: data.data.run_id || 0,
        seq: data.data.seq || 0,
        type: data.data.event?.type || data.data.type,
        timestamp: data.data.timestamp,
        host: data.data.event?.host || data.data.host,
        task: data.data.event?.task || data.data.task,
        play: data.data.event?.play || data.data.play,
        message: data.data.event?.message || data.data.message,
        result: data.data.event?.result || data.data.result,
        changed: data.data.event?.changed || data.data.changed,
        duration: data.data.event?.duration || data.data.duration
      };
      this._events.update(events => [...events, runEvent].sort((a, b) => 
        new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime()
      ));
    } else if (data.type === 'error') {
      this._error.set(data.error || 'Unknown error');
    }
  }

  /**
   * Get events as an observable
   */
  getEventsObservable(): Observable<RunEvent> {
    return new Observable<RunEvent>(subscriber => {
      const subscription = this.events.subscribe(events => {
        // This is a simplified approach - in real usage, you'd want to
        // emit only new events
      });
      
      return () => subscription.unsubscribe();
    });
  }

  /**
   * Get error stream
   */
  getErrors(): Observable<string> {
    return new Observable<string>(subscriber => {
      const subscription = this.error.subscribe(err => {
        if (err) subscriber.next(err);
      });
      
      return () => subscription.unsubscribe();
    });
  }

  /**
   * Disconnect the WebSocket
   */
  disconnect(): void {
    const sock = this._socket();
    if (sock) {
      sock.close();
      this._socket.set(null);
    }
    this._reconnectAttempts.set(0);
  }

  /**
   * Check if connected
   */
  isConnected(): boolean {
    return this._connected();
  }

  /**
   * Clear all events
   */
  clearEvents(): void {
    this._events.set([]);
  }

  /**
   * Cleanup on destroy
   */
  ngOnDestroy(): void {
    this.destroy$.next();
    this.destroy$.complete();
    this.disconnect();
  }
}
