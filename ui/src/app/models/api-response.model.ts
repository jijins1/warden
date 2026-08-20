// Generic API response wrapper
export interface ApiResponse<T> {
  data: T;
  message?: string;
}

// Paginated response
export interface PaginatedResponse<T> {
  items: T[];
  total: number;
  nextCursor?: string;
  hasMore: boolean;
}

// Error response
export interface ApiError {
  error: string;
  message: string;
  statusCode: number;
}

// WebSocket message types
export type WebSocketMessageType = 'event' | 'heartbeat' | 'end' | 'error';

export interface WebSocketMessage {
  type: WebSocketMessageType;
  data?: any;
  error?: string;
}

// WebSocket event message
export interface WebSocketEventMessage {
  type: 'event';
  data: {
    runRef: string;
    runId: number;
    seq: number;
    event: any; // RunEvent
    timestamp: string;
  };
}

// WebSocket end message
export interface WebSocketEndMessage {
  type: 'end';
  data: {
    runRef: string;
    runId: number;
    state: string;
    terminationCause: string;
    changedCount: number;
  };
}
