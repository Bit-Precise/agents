import { useId, type ReactNode } from "react";
import { AlertCircle, Inbox } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  CardDescription,
} from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Alert, AlertDescription } from "@/components/ui/alert";
import type { Health } from "@/lib/model";

export function Status({ label, tone }: Health) {
  return (
    <Badge variant="outline" className={`status status-${tone}`}>
      <span className="status-dot" />
      {label}
    </Badge>
  );
}
export function Empty({
  title = "暂无记录",
  description = "符合当前工作空间与搜索条件的记录会显示在这里。",
  action,
}: {
  title?: string;
  description?: string;
  action?: ReactNode;
}) {
  return (
    <div className="empty-state">
      <div className="empty-icon">
        <Inbox size={23} />
      </div>
      <h3>{title}</h3>
      <p>{description}</p>
      {action}
    </div>
  );
}
export function Notice({
  children,
  error = false,
}: {
  children: ReactNode;
  error?: boolean;
}) {
  return (
    <Alert
      variant={error ? "destructive" : "default"}
      className={error ? "notice notice-error" : "notice"}
    >
      <AlertCircle size={16} />
      <AlertDescription>{children}</AlertDescription>
    </Alert>
  );
}
export function Panel({
  title,
  description,
  action,
  children,
}: {
  title: string;
  description?: string;
  action?: ReactNode;
  children: ReactNode;
}) {
  return (
    <Card className="data-panel">
      <CardHeader className="panel-header">
        <div>
          <CardTitle>{title}</CardTitle>
          {description && <CardDescription>{description}</CardDescription>}
        </div>
        {action}
      </CardHeader>
      <CardContent className="p-0">{children}</CardContent>
    </Card>
  );
}
export function DataTable({
  headers,
  rows,
  empty,
}: {
  headers: string[];
  rows: { key: string; cells: ReactNode[] }[];
  empty?: ReactNode;
}) {
  return !rows.length ? (
    <>{empty || <Empty />}</>
  ) : (
    <Table>
      <TableHeader>
        <TableRow>
          {headers.map((h) => (
            <TableHead key={h}>{h}</TableHead>
          ))}
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((row) => (
          <TableRow key={row.key}>
            {row.cells.map((cell, index) => (
              <TableCell key={index}>{cell}</TableCell>
            ))}
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}
export function Cell({
  title,
  subtitle,
  icon,
}: {
  title: ReactNode;
  subtitle?: ReactNode;
  icon?: ReactNode;
}) {
  return (
    <div className="name-cell">
      {icon && <span className="object-icon">{icon}</span>}
      <div>
        <div className="cell-title">{title}</div>
        {subtitle && <div className="cell-subtitle">{subtitle}</div>}
      </div>
    </div>
  );
}
export function Tags({ items }: { items: string[] }) {
  return (
    <div className="flex flex-wrap gap-1.5">
      {items.length ? (
        items.map((item) => (
          <Badge key={item} variant="secondary" className="capability">
            {item}
          </Badge>
        ))
      ) : (
        <span className="text-xs text-muted-foreground">未启用业务角色</span>
      )}
    </div>
  );
}
export function Field({
  label,
  hint,
  children,
  id,
}: {
  label: string;
  hint?: string;
  children: ReactNode;
  id?: string;
}) {
  return (
    <div className="field">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {hint && <p className="field-hint">{hint}</p>}
    </div>
  );
}
export type Choice = { value: string; label: string; disabled?: boolean };
export function ChoiceSelect({
  value,
  onChange,
  options,
  placeholder = "请选择",
  disabled = false,
  id,
  label,
}: {
  value: string;
  onChange: (value: string) => void;
  options: Choice[];
  placeholder?: string;
  disabled?: boolean;
  id?: string;
  label?: string;
}) {
  const generatedID = useId();
  return (
    <Select
      value={value || null}
      onValueChange={(v) => onChange(v ?? "")}
      disabled={disabled}
      items={options.map((o) => ({ value: o.value, label: o.label }))}
    >
      <SelectTrigger
        id={id || generatedID}
        aria-label={label || placeholder}
        className="w-full"
      >
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent>
        {options.map((o) => (
          <SelectItem key={o.value} value={o.value} disabled={o.disabled}>
            {o.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
