import { Injectable, signal, computed } from '@angular/core';
import { HttpClient, HttpParams } from '@angular/common/http';
import { Observable } from 'rxjs';
import { toSignal, toObservable } from '@angular/core/rxjs-interop';

import { ConfigService } from './config.service';
import { RunListResult, RunFilter, Run, RunListItem } from '../models/run.model';

@Injectable({
  providedIn: 'root'
})
export class RunsService {
  // Signals for state
  private readonly _runs = signal<RunListItem[]>([]);
  private readonly _loading = signal<boolean>(false);
  private readonly _totalCount = signal<number>(0);
  private readonly _error = signal<string | null>(null);
  private readonly _filter = signal<RunFilter>({});
  private readonly _environments = signal<string[]>([]);

  // Public readonly signals
  readonly runs = this._runs.asReadonly();
  readonly loading = this._loading.asReadonly();
  readonly totalCount = this._totalCount.asReadonly();
  readonly error = this._error.asReadonly();
  readonly filter = this._filter.asReadonly();
  readonly environments = this._environments.asReadonly();

  // Computed
  readonly hasRuns = computed(() => this._runs().length > 0);
  readonly hasFilters = computed(() => {
    const f = this._filter();
    return Object.values(f).some(v => v !== undefined && v !== null && v !== '');
  });

  constructor(
    private http: HttpClient,
    private config: ConfigService
  ) {}

  /**
   * List runs with pagination and filtering
   */
  listRuns(filter: RunFilter = {}): Observable<RunListResult> {
    this._loading.set(true);
    this._error.set(null);
    this._filter.set(filter);

    const apiUrl = this.config.apiUrl();
    
    let params = new HttpParams();
    
    if (filter.cursor) {
      params = params.set('cursor', filter.cursor);
    }
    if (filter.limit) {
      params = params.set('limit', filter.limit.toString());
    }
    if (filter.environment) {
      params = params.set('env', filter.environment);
    }
    if (filter.inventory) {
      params = params.set('inventory', filter.inventory);
    }
    if (filter.userId) {
      params = params.set('user_id', filter.userId.toString());
    }
    if (filter.state) {
      params = params.set('state', filter.state);
    }
    if (filter.since) {
      params = params.set('since', filter.since);
    }
    if (filter.until) {
      params = params.set('until', filter.until);
    }
    if (filter.changedMin) {
      params = params.set('changed_min', filter.changedMin.toString());
    }

    const observable = this.http.get<RunListResult>(`${apiUrl}/runs`, { params });
    
    observable.subscribe({
      next: (result) => {
        this._runs.set(result.runs);
        this._totalCount.set(result.runs.length + (result.nextCursor ? 1 : 0));
        this._environments.set([...new Set(result.runs.map(r => r.environment).filter(Boolean))].sort());
        this._loading.set(false);
      },
      error: (err) => {
        this._error.set(err.message || 'Failed to load runs');
        this._loading.set(false);
      }
    });

    return observable;
  }

  /**
   * Get a specific run by its reference
   */
  getRun(runRef: string): Observable<Run> {
    const apiUrl = this.config.apiUrl();
    return this.http.get<Run>(`${apiUrl}/runs/${runRef}`);
  }

  /**
   * Get runs for a specific host
   */
  getRunsByHost(host: string, since?: string, until?: string): Observable<RunListItem[]> {
    const apiUrl = this.config.apiUrl();
    
    let params = new HttpParams();
    if (since) {
      params = params.set('since', since);
    }
    if (until) {
      params = params.set('until', until);
    }

    return this.http.get<RunListItem[]>(`${apiUrl}/hosts/${host}/runs`, { params });
  }

  /**
   * Get the run diff (git diff)
   */
  getRunDiff(runRef: string): Observable<string> {
    const apiUrl = this.config.apiUrl();
    return this.http.get(`${apiUrl}/runs/${runRef}/diff`, { 
      responseType: 'text'
    });
  }

  /**
   * Get a specific event diff
   */
  getEventDiff(runRef: string, seq: number): Observable<string> {
    const apiUrl = this.config.apiUrl();
    return this.http.get(`${apiUrl}/runs/${runRef}/events/${seq}/diff`, { 
      responseType: 'text'
    });
  }

  /**
   * Update filter
   */
  updateFilter(filter: Partial<RunFilter>): void {
    this._filter.update(f => ({ ...f, ...filter }));
  }

  /**
   * Clear all filters
   */
  clearFilters(): void {
    this._filter.set({});
  }

  /**
   * Apply filters (reload data)
   */
  applyFilters(): void {
    this.listRuns(this._filter());
  }
}
