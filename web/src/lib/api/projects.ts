import { get, post } from '@/lib/api/client';
import type {
  CreateProjectInput,
  Paginated,
  Project,
  ProjectSearchParams,
  UUID,
} from '@/lib/types';

/** Project service — wraps /v1/projects. */
export const projectsApi = {
  search(params: ProjectSearchParams = {}): Promise<Paginated<Project>> {
    return get<Paginated<Project>>('/v1/projects', {
      params: {
        q: params.q,
        skills: params.skills,
        budget_type: params.budget_type,
        min_budget: params.min_budget,
        cursor: params.cursor,
        limit: params.limit ?? 20,
      },
      // serialize repeated `skills` as ?skills=a&skills=b
      paramsSerializer: { indexes: null },
    });
  },

  get(id: UUID): Promise<Project> {
    return get<Project>(`/v1/projects/${id}`);
  },

  create(input: CreateProjectInput): Promise<Project> {
    return post<Project>('/v1/projects', input);
  },
};
