import { Component } from '@angular/core';
import { CommonModule } from '@angular/common';
import { RouterOutlet } from '@angular/router';
import { MatToolbarModule } from '@angular/material/toolbar';
import { MatIconModule } from '@angular/material/icon';
import { MatButtonModule } from '@angular/material/button';
import { MatSidenavModule } from '@angular/material/sidenav';
import { MatListModule } from '@angular/material/list';
import { MatDividerModule } from '@angular/material/divider';
import { RouterModule } from '@angular/router';

@Component({
  selector: 'app-root',
  standalone: true,
  imports: [
    CommonModule,
    RouterOutlet,
    MatToolbarModule,
    MatIconModule,
    MatButtonModule,
    MatSidenavModule,
    MatListModule,
    MatDividerModule,
    RouterModule
  ],
  template: `
    <mat-sidenav-container class="sidenav-container">
      <mat-sidenav #sidenav mode="side" opened class="sidenav">
        <mat-nav-list>
          <a mat-list-item routerLink="/" routerLinkActive="active-link">
            <mat-icon matListIcon>dashboard</mat-icon>
            <span matLine>Tableau de bord</span>
          </a>
          <a mat-list-item routerLink="/runs" routerLinkActive="active-link">
            <mat-icon matListIcon>play_arrow</mat-icon>
            <span matLine>Exécutions</span>
          </a>
          <a mat-list-item routerLink="/hosts" routerLinkActive="active-link">
            <mat-icon matListIcon>dns</mat-icon>
            <span matLine>Machines</span>
          </a>
          <mat-divider></mat-divider>
          <a mat-list-item routerLink="/environments" routerLinkActive="active-link">
            <mat-icon matListIcon>folder</mat-icon>
            <span matLine>Environnements</span>
          </a>
        </mat-nav-list>
      </mat-sidenav>
      
      <mat-sidenav-content>
        <mat-toolbar color="primary" class="toolbar">
          <button mat-icon-button (click)="sidenav.toggle()">
            <mat-icon>menu</mat-icon>
          </button>
          <span class="title">Warden - Audit</span>
          <span class="spacer"></span>
          <button mat-icon-button [matMenuTriggerFor]="userMenu">
            <mat-icon>account_circle</mat-icon>
          </button>
        </mat-toolbar>
        
        <div class="content">
          <router-outlet></router-outlet>
        </div>
      </mat-sidenav-content>
    </mat-sidenav-container>
  `,
  styles: [`
    .sidenav-container {
      height: 100%;
    }
    
    .sidenav {
      width: 240px;
      background-color: #f5f5f5;
    }
    
    .toolbar {
      position: sticky;
      top: 0;
      z-index: 2;
    }
    
    .title {
      margin-left: 16px;
      font-size: 1.2rem;
      font-weight: 500;
    }
    
    .spacer {
      flex: 1 1 auto;
    }
    
    .content {
      padding: 24px;
      height: calc(100% - 64px);
      overflow-y: auto;
    }
    
    .active-link {
      background-color: rgba(0, 0, 0, 0.04);
    }
  `]
})
export class AppComponent {
}
