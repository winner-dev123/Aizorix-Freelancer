import Link from 'next/link';

import { Badge } from '@/components/ui/Badge';
import { Card } from '@/components/ui/Card';
import { formatBudgetRange, formatRelative } from '@/lib/format';
import type { Project } from '@/lib/types';

export interface ProjectCardProps {
  project: Project;
  /** Where the title links to; defaults to the project detail route. */
  href?: string;
}

export function ProjectCard({ project, href }: ProjectCardProps) {
  const target = href ?? `/projects/${project.id}`;
  return (
    <Card className="p-5 transition-shadow hover:shadow-md">
      <div className="flex items-start justify-between gap-3">
        <Link href={target} className="group">
          <h3 className="text-base font-semibold text-slate-900 group-hover:text-brand-600">
            {project.title}
          </h3>
        </Link>
        <Badge tone={project.budget_type === 'hourly' ? 'info' : 'brand'}>
          {project.budget_type === 'hourly' ? 'Hourly' : 'Fixed'}
        </Badge>
      </div>

      <p className="mt-2 line-clamp-2 text-sm text-muted">{project.description}</p>

      <div className="mt-4 flex flex-wrap gap-1.5">
        {project.skills.slice(0, 5).map((skill) => (
          <Badge key={skill} tone="neutral">
            {skill}
          </Badge>
        ))}
        {project.skills.length > 5 && (
          <Badge tone="neutral">+{project.skills.length - 5}</Badge>
        )}
      </div>

      <div className="mt-4 flex items-center justify-between border-t border-slate-100 pt-4">
        <span className="text-sm font-semibold text-slate-900">
          {formatBudgetRange(
            project.budget_min_cents,
            project.budget_max_cents,
            project.currency,
            project.budget_type === 'hourly',
          )}
        </span>
        <span className="text-xs text-muted">
          {typeof project.proposals_count === 'number' && (
            <>{project.proposals_count} proposals · </>
          )}
          {project.created_at ? formatRelative(project.created_at) : 'Just now'}
        </span>
      </div>
    </Card>
  );
}
