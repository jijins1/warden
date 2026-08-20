import { Component, OnInit, OnDestroy, signal, computed, effect } from '@angular/core';
import { CommonModule } from '@angular/common';
import { MatCardModule } from '@angular/material/card';
import { MatTabsModule } from '@angular/material/tabs';
import { MatButtonModule } from '@angular/material/button';
import { MatIconModule } from '@angular/material/icon';
import { MatProgressSpinnerModule } from '@angular/material/progress-spinner';
import { MatChipsModule } from '@angular/material/chips';
import { MatDividerModule } from '@angular/material/divider';
import { MatExpansionModule } from '@angular/material/expansion';
import { MatListModule } from '@angular/material/list';
import { MatTooltipModule } from '@angular/material/tooltip';
import { ActivatedRoute, RouterModule } from '@angular/router';
import { Run, RunEvent, RunState } from '../../models/run.model';
import { RunsService } from '../../services/runs.service';
import { EventsService } from '../../services/events.service';

@Component({
  selector: 'app-run-detail-page',
  standalone: true,
  imports: [
    CommonModule,
    MatCardModule,
    MatTabsModule,
    MatButtonModule,
    MatIconModule,
    MatProgressSpinnerModule,
    MatChipsModule,
    MatDividerModule,
    MatExpansionModule,
    MatListModule,
    MatTooltipModule,
    RouterModule
  ],
  template: `
    <div class="run-detail-page">
      <div *ngIf="loading()" class="loading-spinner">
        <mat-spinner diameter="50"></mat-spinner>
      </div>

      <div *ngIf="!loading() && run()" class="content">
        <!-- Header -->
        <mat-card class="header-card">
          <mat-card-content>
            <div class="header-content">
              <div class="header-main">
                <h1 class="run-ref">{{ run()?.runRef }}</h1>
                <span [class]="'status-badge ' + run()?.state">{{ run()?.state }}</span>
              </div>
              
              <div class="header-meta">
                <div class="meta-item">
                  <mat-icon>play_arrow</mat-icon>
                  <span>{{ run()?.playbook }}</span>
                </div>
                <div class="meta-item">
                  <mat-icon>folder</mat-icon>
                  <span>{{ run()?.environment }}</span>
                </div>
                <div class="meta-item">
                  <mat-icon>dns</mat-icon>
                  <span>{{ run()?.inventory }}</span>
                </div>
                <div class="meta-item">
                  <mat-icon>code</mat-icon>
                  <span class="truncate">{{ run()?.repo }}</span>
                </div>
                <div class="meta-item">
                  <mat-icon>person</mat-icon>
                  <span>Utilisateur #{{ run()?.userId }}</span>
                </div>
              </div>

              <mat-divider></mat-divider>

              <div class="header-timing">
                <div class="timing-item">
                  <span class="label">Début:</span>
                  <span class="value">{{ formatDate(run()!.runDay) }}</span>
                </div>
                <div class="timing-item">
                  <span class="label">Durée:</span>
                  <span class="value">{{ calculateDuration() }}</span>
                </div>
                <div class="timing-item">
                  <span class="label">Changements:</span>
                  <span class="value">{{ run()?.changedCount }}</span>
                </div>
                <div class="timing-item" *ngIf="run()?.secretsUnmasked">
                  <mat-icon color="warn" matTooltip="Les secrets sont visibles dans cet audit">warning</mat-icon>
                  <span class="label">Secrets visibles</span>
                </div>
              </div>
            </div>
          </mat-card-content>
        </mat-card>

        <!-- Tabs -->
        <mat-tab-group>
          <!-- Events Tab -->
          <mat-tab label="Événements">
            <div class="events-container">
              <div *ngIf="eventsService.connected() === false" class="events-loading">
                <mat-spinner diameter="30"></mat-spinner>
                <span>Connexion au stream...</span>
              </div>

              <div class="events-list">
                <div *ngFor="let event of eventsService.events()" class="event-item" [class.changed]="event.changed">
                  <div class="event-header">
                    <span class="event-type">{{ event.type }}</span>
                    <span class="event-timestamp">{{ formatDateTime(event.timestamp) }}</span>
                    <span class="event-host" *ngIf="event.host">{{ event.host }}</span>
                  </div>
                  <div class="event-body">
                    <div class="event-task" *ngIf="event.task">{{ event.task }}</div>
                    <div class="event-message" *ngIf="event.message">{{ event.message }}</div>
                    <div class="event-result" *ngIf="event.result">{{ event.result }}</div>
                    <div class="event-duration" *ngIf="event.duration">{{ event.duration }}ms</div>
                  </div>
                </div>

                <div *ngIf="eventsService.events().length === 0" class="empty-events">
                  <mat-icon>inbox</mat-icon>
                  <p>Aucun événement reçu</p>
                </div>
              </div>
            </div>
          </mat-tab>

          <!-- Diff Tab -->
          <mat-tab label="Diff">
            <div class="diff-container">
              <div *ngIf="diffLoading()" class="diff-loading">
                <mat-spinner diameter="30"></mat-spinner>
              </div>
              <div *ngIf="!diffLoading()" class="diff-content">
                <pre *ngIf="diff()" class="diff-viewer">{{ diff() }}</pre>
                <div *ngIf="!diff()" class="empty-diff">
                  <mat-icon>code</mat-icon>
                  <p>Aucun diff disponible</p>
                </div>
              </div>
            </div>
          </mat-tab>

          <!-- Summary Tab -->
          <mat-tab label="Résumé">
            <div class="summary-container">
              <mat-card>
                <mat-card-content>
                  <h3>Informations générales</h3>
                  <div class="summary-grid">
                    <div class="summary-item">
                      <span class="label">Référence:</span>
                      <span class="value">{{ run()?.runRef }}</span>
                    </div>
                    <div class="summary-item">
                      <span class="label">SHA Audit:</span>
                      <span class="value truncate">{{ run()?.auditSHA }}</span>
                    </div>
                    <div class="summary-item">
                      <span class="label">SHA Base:</span>
                      <span class="value truncate">{{ run()?.baseSHA }}</span>
                    </div>
                    <div class="summary-item">
                      <span class="label">Référence principale:</span>
                      <span class="value">{{ run()?.mainlineRef }}</span>
                    </div>
                    <div class="summary-item">
                      <span class="label">SHA principale:</span>
                      <span class="value truncate">{{ run()?.mainlineSHA }}</span>
                    </div>
                    <div class="summary-item">
                      <span class="label">Cause de terminaison:</span>
                      <span class="value">{{ run()?.terminationCause }}</span>
                    </div>
                    <div class="summary-item" *ngIf="run()?.artifactPrunedAt">
                      <span class="label">Artefact purgé:</span>
                      <span class="value">{{ formatDate(run()!.artifactPrunedAt) }}</span>
                    </div>
                  </div>
                </mat-card-content>
              </mat-card>
            </div>
          </mat-tab>
        </mat-tab-group>

        <!-- Back Button -->
        <div class="back-button">
          <button mat-button color="primary" routerLink="/runs">
            <mat-icon>arrow_back</mat-icon>
            Retour à la liste
          </button>
        </div>
      </div>

      <div *ngIf="!loading() && !run()" class="not-found">
        <mat-icon>error_outline</mat-icon>
        <h2>Exécution non trouvée</h2>
        <p>L'exécution avec la référence "{{ runRef() }}" n'existe pas ou a été supprimée.</p>
        <button mat-button color="primary" routerLink="/runs">Retour à la liste</button>
      </div>
    </div>
  `,
  styles: [`
    .run-detail-page {
      max-width: 1400px;
      margin: 0 auto;
    }

    .loading-spinner {
      display: flex;
      justify-content: center;
      align-items: center;
      height: 200px;
    }

    .header-card {
      margin-bottom: 20px;
    }

    .header-content {
      display: flex;
      flex-direction: column;
      gap: 16px;
    }

    .header-main {
      display: flex;
      align-items: center;
      gap: 16px;
    }

    .run-ref {
      margin: 0;
      font-size: 1.5rem;
      font-weight: 500;
    }

    .status-badge {
      padding: 6px 16px;
      border-radius: 16px;
      font-size: 0.875rem;
      font-weight: 500;
      text-transform: uppercase;

      &.pending, &.running {
        background-color: #fff3e0;
        color: #f57c00;
      }
      &.succeeded {
        background-color: #e8f5e9;
        color: #2e7d32;
      }
      &.failed {
        background-color: #ffebee;
        color: #c62828;
      }
      &.cancelled {
        background-color: #fff3e0;
        color: #f57c00;
      }
    }

    .header-meta {
      display: flex;
      flex-wrap: wrap;
      gap: 20px;
    }

    .meta-item {
      display: flex;
      align-items: center;
      gap: 8px;
      font-size: 0.9rem;
      color: rgba(0, 0, 0, 0.7);

      mat-icon {
        font-size: 18px;
        width: 18px;
        height: 18px;
      }
    }

    .header-timing {
      display: flex;
      flex-wrap: wrap;
      gap: 20px;
      font-size: 0.9rem;
    }

    .timing-item {
      display: flex;
      align-items: center;
      gap: 8px;
    }

    .label {
      font-weight: 500;
      color: rgba(0, 0, 0, 0.6);
    }

    .value {
      color: #333;
    }

    mat-tab-group {
      margin-bottom: 20px;
    }

    .events-container {
      padding: 20px;
      max-height: 600px;
      overflow-y: auto;
    }

    .events-loading {
      display: flex;
      align-items: center;
      gap: 16px;
      padding: 20px;
      color: rgba(0, 0, 0, 0.6);
    }

    .events-list {
      display: flex;
      flex-direction: column;
      gap: 12px;
    }

    .event-item {
      padding: 12px;
      border-radius: 4px;
      background-color: #fafafa;
      border-left: 4px solid #e0e0e0;

      &.changed {
        border-left-color: #2e7d32;
      }
    }

    .event-header {
      display: flex;
      align-items: center;
      gap: 12px;
      margin-bottom: 8px;
      font-size: 0.875rem;
    }

    .event-type {
      font-weight: 500;
      color: #1565c0;
    }

    .event-timestamp {
      color: rgba(0, 0, 0, 0.5);
      font-size: 0.8rem;
    }

    .event-host {
      padding: 2px 8px;
      background-color: #e3f2fd;
      border-radius: 4px;
      font-size: 0.8rem;
    }

    .event-body {
      display: flex;
      flex-direction: column;
      gap: 4px;
    }

    .event-task {
      font-weight: 500;
    }

    .event-message, .event-result {
      font-size: 0.9rem;
      color: rgba(0, 0, 0, 0.7);
    }

    .event-duration {
      font-size: 0.8rem;
      color: rgba(0, 0, 0, 0.5);
    }

    .empty-events {
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      padding: 40px;
      color: rgba(0, 0, 0, 0.6);

      mat-icon {
        font-size: 48px;
        margin-bottom: 16px;
      }
    }

    .diff-container {
      padding: 20px;
      max-height: 600px;
      overflow-y: auto;
      background-color: #1e1e1e;
      border-radius: 4px;
    }

    .diff-loading {
      display: flex;
      justify-content: center;
      padding: 20px;
    }

    .diff-viewer {
      margin: 0;
      padding: 16px;
      overflow-x: auto;
      font-size: 0.85rem;
      line-height: 1.5;
    }

    .empty-diff {
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      padding: 40px;
      color: rgba(255, 255, 255, 0.6);

      mat-icon {
        font-size: 48px;
        margin-bottom: 16px;
      }
    }

    .summary-container {
      padding: 20px;
    }

    .summary-grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
      gap: 16px;
    }

    .summary-item {
      display: flex;
      justify-content: space-between;
      padding: 8px 0;
      border-bottom: 1px solid #e0e0e0;
    }

    .back-button {
      margin-top: 20px;
    }

    .not-found {
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      padding: 40px;
      text-align: center;
      color: rgba(0, 0, 0, 0.6);

      mat-icon {
        font-size: 64px;
        margin-bottom: 16px;
      }

      h2 {
        margin: 0 0 16px;
      }

      button {
        margin-top: 16px;
      }
    }
  `]
})
export class RunDetailPageComponent implements OnInit, OnDestroy {
  // Signals for state
  private readonly _runRef = signal<string>('');
  private readonly _run = signal<Run | null>(null);
  private readonly _loading = signal<boolean>(true);
  private readonly _diff = signal<string | null>(null);
  private readonly _diffLoading = signal<boolean>(true);

  // Public readonly signals
  readonly runRef = this._runRef.asReadonly();
  readonly run = this._run.asReadonly();
  readonly loading = this._loading.asReadonly();
  readonly diff = this._diff.asReadonly();
  readonly diffLoading = this._diffLoading.asReadonly();

  constructor(
    private route: ActivatedRoute,
    private runsService: RunsService,
    public eventsService: EventsService
  ) {}

  ngOnInit(): void {
    this._runRef.set(this.route.snapshot.paramMap.get('runRef') || '');
    this.loadRun();
    this.connectToEvents();
    this.loadDiff();
  }

  ngOnDestroy(): void {
    this.eventsService.disconnect();
    this.eventsService.clearEvents();
  }

  loadRun(): void {
    this._loading.set(true);
    this.runsService.getRun(this._runRef()).subscribe({
      next: (run) => {
        this._run.set(run);
        this._loading.set(false);
      },
      error: () => {
        this._loading.set(false);
      }
    });
  }

  connectToEvents(): void {
    this.eventsService.connectToRun(this._runRef());
  }

  loadDiff(): void {
    this._diffLoading.set(true);
    this.runsService.getRunDiff(this._runRef()).subscribe({
      next: (diff) => {
        this._diff.set(diff);
        this._diffLoading.set(false);
      },
      error: () => {
        this._diffLoading.set(false);
      }
    });
  }

  formatDate(dateString: string): string {
    const date = new Date(dateString);
    return date.toLocaleDateString('fr-FR', {
      year: 'numeric',
      month: 'long',
      day: 'numeric'
    });
  }

  formatDateTime(dateString: string): string {
    const date = new Date(dateString);
    return date.toLocaleString('fr-FR', {
      year: 'numeric',
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit'
    });
  }

  calculateDuration(): string {
    const run = this._run();
    if (!run) return '-';
    
    const start = new Date(run.runDay);
    const end = run.endedAt ? new Date(run.endedAt) : new Date();
    
    const durationMs = end.getTime() - start.getTime();
    
    if (durationMs < 60000) {
      return `${Math.round(durationMs / 1000)}s`;
    }
    if (durationMs < 3600000) {
      return `${Math.round(durationMs / 60000)}min`;
    }
    return `${Math.round(durationMs / 3600000)}h`;
  }
}
