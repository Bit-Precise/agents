import type { ReactNode } from "react";
import { BotIcon } from "lucide-react";
import type { Principal, User } from "@/api/generated";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { useEntity, useName } from "@/lib/names";
import { initials, roleNames, short } from "@/lib/format";

export function NameCell({
  title,
  subtitle,
}: {
  title: ReactNode;
  subtitle?: ReactNode;
}) {
  return (
    <div className="grid gap-0.5">
      <span className="font-medium">{title}</span>
      {subtitle && (
        <span className="text-xs text-muted-foreground">{subtitle}</span>
      )}
    </div>
  );
}

export function UserAvatar({
  user,
  className,
}: {
  user?: User;
  className?: string;
}) {
  return (
    <Avatar className={className}>
      {user?.picture && <AvatarImage src={user.picture} alt={user.name} />}
      <AvatarFallback>{initials(user?.name)}</AvatarFallback>
    </Avatar>
  );
}

export function UserCell({
  id,
  workspaceId,
}: {
  id?: string;
  workspaceId?: string;
}) {
  const user = useEntity("users", id, workspaceId);
  return (
    <div className="flex items-center gap-2">
      <UserAvatar user={user} className="size-7" />
      <NameCell title={user?.name ?? short(id)} subtitle={user?.email} />
    </div>
  );
}

// Who a device runs as: a person, or a Service Account of its Workspace.
export function PrincipalCell({
  principal,
  workspaceId,
}: {
  principal?: Principal;
  workspaceId?: string;
}) {
  const sa = principal?.type === "service_account";
  const name = useName(
    sa ? "serviceAccounts" : "users",
    principal?.id,
    workspaceId,
  );
  if (!principal)
    return <span className="text-muted-foreground">匿名访问者</span>;
  return sa ? (
    <span className="inline-flex items-center gap-1.5">
      <BotIcon className="size-4 text-muted-foreground" />
      {name}
    </span>
  ) : (
    <UserCell id={principal.id} workspaceId={workspaceId} />
  );
}

export function Name({
  kind,
  id,
  workspaceId,
}: {
  kind: Parameters<typeof useName>[0];
  id?: string;
  workspaceId?: string;
}) {
  return <>{useName(kind, id, workspaceId)}</>;
}

export function RoleBadges({ roles }: { roles: readonly string[] }) {
  if (!roles.length) return <span className="text-muted-foreground">—</span>;
  return (
    <div className="flex flex-wrap gap-1">
      {roles.map((role) => (
        <Badge key={role} variant="outline">
          {roleNames[role] ?? role}
        </Badge>
      ))}
    </div>
  );
}

export type Tone = "active" | "muted" | "warning" | "danger";
export function StatusBadge({ label, tone }: { label: string; tone: Tone }) {
  const variant =
    tone === "active"
      ? "secondary"
      : tone === "danger"
        ? "destructive"
        : "outline";
  return (
    <Badge
      variant={variant}
      className={tone === "muted" ? "text-muted-foreground" : ""}
    >
      {tone === "active" && (
        <span className="size-1.5 rounded-full bg-emerald-500" />
      )}
      {tone === "warning" && (
        <span className="size-1.5 rounded-full bg-amber-500" />
      )}
      {label}
    </Badge>
  );
}
