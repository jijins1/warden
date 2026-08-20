import { Injectable } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { Observable } from 'rxjs';

import { ConfigService } from './config.service';
import { Environment, EnvironmentDetail } from '../models/environment.model';

@Injectable({
  providedIn: 'root'
})
export class EnvironmentsService {
  constructor(
    private http: HttpClient,
    private config: ConfigService
  ) {}

  /**
   * List all environments
   */
  listEnvironments(): Observable<Environment[]> {
    const apiUrl = this.config.getApiUrl();
    return this.http.get<Environment[]>(`${apiUrl}/admin/environments`);
  }

  /**
   * Get a specific environment with details
   */
  getEnvironment(name: string): Observable<EnvironmentDetail> {
    const apiUrl = this.config.getApiUrl();
    return this.http.get<EnvironmentDetail>(`${apiUrl}/admin/environments/${name}`);
  }

  /**
   * Create a new environment
   */
  createEnvironment(environment: { name: string; description?: string }): Observable<Environment> {
    const apiUrl = this.config.getApiUrl();
    return this.http.post<Environment>(`${apiUrl}/admin/environments`, environment);
  }

  /**
   * Update an environment
   */
  updateEnvironment(name: string, environment: Environment): Observable<Environment> {
    const apiUrl = this.config.getApiUrl();
    return this.http.put<Environment>(`${apiUrl}/admin/environments/${name}`, environment);
  }

  /**
   * Delete an environment
   */
  deleteEnvironment(name: string): Observable<void> {
    const apiUrl = this.config.getApiUrl();
    return this.http.delete<void>(`${apiUrl}/admin/environments/${name}`);
  }

  /**
   * Get statistics for an environment
   */
  getEnvironmentStats(name: string): Observable<any> {
    const apiUrl = this.config.getApiUrl();
    return this.http.get<any>(`${apiUrl}/admin/environments/${name}/stats`);
  }
}
