import { TestBed } from '@angular/core/testing';
import { describe, expect, it } from 'vitest';
import { TagsRun } from './tags-run';

describe('TagsRun', () => {
  it('ne montre rien quand le run a joué le playbook en entier', async () => {
    const fixture = TestBed.createComponent(TagsRun);
    fixture.componentRef.setInput('flags', ['--check', '--limit=web']);
    await fixture.whenStable();

    expect((fixture.nativeElement as HTMLElement).querySelector('.w-scope')).toBeNull();
  });

  it('montre chaque tag, et distingue ceux qui ont été sautés', async () => {
    const fixture = TestBed.createComponent(TagsRun);
    fixture.componentRef.setInput('flags', ['--tags=deploy,config', '--skip-tags=slow']);
    await fixture.whenStable();

    const el = fixture.nativeElement as HTMLElement;
    const joues = [...el.querySelectorAll('.w-tag:not(.w-tag--saute)')].map((t) =>
      t.textContent?.trim(),
    );
    const sautes = [...el.querySelectorAll('.w-tag--saute')].map((t) => t.textContent?.trim());
    expect(joues).toEqual(['deploy', 'config']);
    expect(sautes).toEqual(['slow']);
    expect(el.textContent).toContain('only part of the playbook');
  });
});
