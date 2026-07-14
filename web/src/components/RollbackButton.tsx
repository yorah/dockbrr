import { Button } from "@/components/ui/button";
import { useRollback } from "@/hooks/mutations";

export interface RollbackButtonProps {
  originalJobId: number;
  /** Called with the rollback job's id so the caller can swap the panel to it. */
  onRollback: (newJobId: number) => void;
}

export function RollbackButton({ originalJobId, onRollback }: RollbackButtonProps) {
  const rollback = useRollback();

  async function handleClick() {
    const res = await rollback.mutateAsync(originalJobId);
    onRollback(res.job_id);
  }

  return (
    <Button variant="destructive" disabled={rollback.isPending} onClick={handleClick}>
      {rollback.isPending ? "Rolling back…" : "Rollback"}
    </Button>
  );
}
