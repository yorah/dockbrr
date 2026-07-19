import { useEffect, useRef, useState } from "react";
import { notify } from "@/lib/notify";
import { useSettings } from "@/hooks/queries";
import { useSaveSettings } from "@/hooks/mutations";
import type { Settings } from "@/api/types";

/** The string-valued, user-editable settings keys (everything but the metadata fields). */
export type SettingKey = Exclude<keyof Settings, "github_token_set" | "restart_required" | "defaults">;

/**
 * Form state for a slice of the settings object: seeds from the server value,
 * tracks which fields diverge from it (the unsaved-changes indicator), and PUTs
 * only the changed keys so two settings pages editing different slices can never
 * clobber each other's fields.
 */
export function useSettingsForm(editableKeys: SettingKey[]) {
  const { data } = useSettings();
  const save = useSaveSettings();
  const [form, setForm] = useState<Partial<Record<SettingKey, string>>>({});
  // The server value each field of the form was last seeded from. NOT
  // necessarily the latest `data`. Used only to decide, PER KEY, whether a
  // fresh `data` may safely overwrite that field: comparing against the live
  // `data` instead would false-positive on every value a background refetch
  // actually changed, even for a form the user never touched (see the re-seed
  // effect below).
  const seededRef = useRef<Settings | undefined>(undefined);

  const changed = (): Record<string, string> => {
    const patch: Record<string, string> = {};
    if (!data) return patch;
    for (const key of editableKeys) {
      const value = form[key];
      if (value !== undefined && value !== data[key]) patch[key] = value;
    }
    return patch;
  };

  const dirty = Object.keys(changed()).length > 0;

  useEffect(() => {
    if (!data) return;
    const baseline = seededRef.current;
    // Re-seed PER KEY. A field the user diverged from its baseline keeps the
    // typed value (a background refetch, window-focus, another tab saving, a
    // settings import, must not silently wipe out a pending edit while
    // "Unsaved changes" is showing); every other field follows the server.
    // Skipping the whole re-seed once ANY field is dirty would leave the
    // untouched fields pinned to a stale seed while `changed()` diffs them
    // against the NEW `data`, dragging stale values into the PUT body and
    // reverting whatever the server had just been told.
    setForm((f) => {
      const next = { ...f };
      for (const k of editableKeys) {
        const edited = baseline !== undefined && f[k] !== undefined && f[k] !== baseline[k];
        if (!edited) next[k] = data[k];
      }
      return next;
    });
    // The baseline ALWAYS advances: "dirty" and "what to send" must be measured
    // against the same server snapshot, or they drift apart.
    seededRef.current = data;
    // editableKeys is a stable literal per page; keyed on its contents, not
    // identity. `form` is read inside the updater rather than listed as a
    // dependency. It must reflect whatever the user has typed as of the render
    // in which `data` changed, not re-run the effect on every keystroke.
  }, [data, editableKeys.join(",")]);

  return {
    data,
    form,
    dirty,
    isSaving: save.isPending,
    setField: (key: SettingKey, value: string) => setForm((f) => ({ ...f, [key]: value })),
    // A field still holding the server-side default reads differently from one
    // deliberately set to that same number.
    isDefault: (key: SettingKey) => {
      const value = form[key];
      return value !== undefined && value === data?.defaults?.[key];
    },
    save: (extra?: Record<string, string>, onSaved?: () => void) => {
      const patch = { ...changed(), ...(extra ?? {}) };
      if (Object.keys(patch).length === 0) return;
      save.mutate(patch, {
        onSuccess: () => {
          notify.success("Settings saved");
          if ("concurrency" in patch) notify.info("Concurrency applies after restart");
          onSaved?.();
        },
      });
    },
  };
}
