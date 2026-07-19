import { useState } from "react";
import { notify } from "@/lib/notify";
import { useChangePassword } from "@/hooks/mutations";
import { ApiError } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { SettingsCard } from "@/components/settings/SettingsCard";

export function PasswordSettings() {
  const changePassword = useChangePassword();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");

  const canSave = current.length > 0 && next.length >= 8 && next === confirm;

  // Only a 401 means the current password was wrong; any other failure (e.g. a
  // 500) must not be misreported as a bad-password error.
  const err = changePassword.error;
  const errorMessage = changePassword.isError
    ? err instanceof ApiError && err.status === 401
      ? "Current password is incorrect"
      : "Couldn't change password. Please try again."
    : null;

  return (
    <SettingsCard title="Password" description="Change the password for this account.">
      <form
        className="max-w-sm space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (!canSave) return;
          changePassword.mutate(
            { current, new: next },
            {
              onSuccess: () => {
                notify.success("Password changed");
                setCurrent("");
                setNext("");
                setConfirm("");
              },
            },
          );
        }}
      >
        <div className="space-y-1">
          <Label htmlFor="current-password">Current password</Label>
          <Input
            id="current-password"
            type="password"
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
          />
        </div>
        <div className="space-y-1">
          <Label htmlFor="new-password">New password</Label>
          <Input
            id="new-password"
            type="password"
            value={next}
            onChange={(e) => setNext(e.target.value)}
          />
        </div>
        <div className="space-y-1">
          <Label htmlFor="confirm-password">Confirm new password</Label>
          <Input
            id="confirm-password"
            type="password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
          />
        </div>
        {errorMessage && (
          <p role="alert" className="text-sm text-danger">{errorMessage}</p>
        )}
        <Button type="submit" disabled={!canSave || changePassword.isPending}>Save</Button>
      </form>
    </SettingsCard>
  );
}
