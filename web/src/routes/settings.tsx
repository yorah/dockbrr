import { SettingsLayout } from "@/components/settings/SettingsLayout";

export function SettingsScreen() {
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4">
      <h1 className="text-xl font-semibold">Settings</h1>
      <SettingsLayout />
    </div>
  );
}
