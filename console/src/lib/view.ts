import type {
  Agent,
  Audit,
  ConnectionStatus,
  EgressRoute,
  EnrollmentKey,
  FlowStatus,
  Grant,
  Membership,
  Pagination,
  Service,
  ServiceAccount,
  User,
  Workspace,
  IngressView,
  EgressView,
  ServerView,
  RouteView,
} from "@/api/generated";

// A browser composition of independently fetched resources, never an API payload.
export type ConsoleView = {
  revision: number;
  serverURL: string;
  edgeAvailable: boolean;
  user: User;
  users: User[];
  workspaces: Workspace[];
  memberships: Membership[];
  agents: Agent[];
  serviceAccounts: ServiceAccount[];
  enrollmentKeys: EnrollmentKey[];
  services: Service[];
  egressRoutes: EgressRoute[];
  grants: Grant[];
  audit: Audit[];
  connections: ConnectionStatus[];
  flows: FlowStatus[];
  ingresses: IngressView[];
  servers: ServerView[];
  egresses: EgressView[];
  routes: RouteView[];
  primaryItems: unknown[];
  pagination: Pagination;
  totals: Record<string, number>;
};
export function emptyView(user: User, serverURL: string): ConsoleView {
  return {
    user,
    serverURL,
    revision: 0,
    edgeAvailable: true,
    users: [user],
    workspaces: [],
    memberships: [],
    agents: [],
    serviceAccounts: [],
    enrollmentKeys: [],
    services: [],
    egressRoutes: [],
    grants: [],
    audit: [],
    connections: [],
    flows: [],
    ingresses: [],
    servers: [],
    egresses: [],
    routes: [],
    primaryItems: [],
    pagination: { page: 1, pageSize: 20, total: 0, totalPages: 0 },
    totals: {},
  };
}
