import type { ReactNode } from "react";
import type { FilterState } from "@/hooks/useDashboardRows";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

export interface FiltersProps {
  value: FilterState;
  onChange: (next: FilterState) => void;
  /** Right-aligned action buttons (e.g. global Check all / Apply all). */
  actions?: ReactNode;
}

const STATUS_OPTIONS = [
  { value: "any", label: "Any status" },
  { value: "update-available", label: "Update available" },
  { value: "up-to-date", label: "Up to date" },
  { value: "pinned", label: "Pinned" },
  { value: "stopped", label: "Stopped" },
  { value: "restarting", label: "Restarting" },
  { value: "gone", label: "Gone" },
];

export function Filters({ value, onChange, actions }: FiltersProps) {
  return (
    <div className="mb-4 flex flex-wrap items-center gap-3">
      <label className="flex items-center gap-2 text-sm">
        <Switch
          checked={value.onlyUpdates}
          onCheckedChange={(checked) => onChange({ ...value, onlyUpdates: checked === true })}
        />
        Only updates
      </label>

      <Select
        value={value.status || "any"}
        onValueChange={(v) => onChange({ ...value, status: v === "any" ? "" : v })}
      >
        <SelectTrigger className="w-44" aria-label="Filter by status">
          <SelectValue placeholder="Any status" />
        </SelectTrigger>
        <SelectContent>
          {STATUS_OPTIONS.map((o) => (
            <SelectItem key={o.value} value={o.value}>
              {o.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>

      <Input
        placeholder="Search service, project, or image…"
        value={value.search}
        onChange={(e) => onChange({ ...value, search: e.target.value })}
        className="w-64"
        aria-label="Search"
      />

      <label className="flex items-center gap-2 text-sm">
        <Switch
          checked={value.showRemoved}
          onCheckedChange={(checked) => onChange({ ...value, showRemoved: checked === true })}
        />
        Show removed
      </label>

      {actions && <div className="ml-auto flex items-center gap-1">{actions}</div>}
    </div>
  );
}
