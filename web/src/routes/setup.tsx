import { useState } from "react";
import { useSetup } from "@/hooks/mutations";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

export function SetupScreen() {
  const setup = useSetup();
  const [u, setU] = useState("");
  const [p, setP] = useState("");
  const tooShort = p.length > 0 && p.length < 8;
  return (
    <div className="mx-auto mt-24 max-w-sm space-y-4">
      <h1 className="text-xl font-semibold">Create admin</h1>
      <form
        className="space-y-3"
        onSubmit={(e) => { e.preventDefault(); if (!tooShort && u && p) setup.mutate({ username: u, password: p }); }}
      >
        <Input aria-label="Username" value={u} onChange={(e) => setU(e.target.value)} placeholder="Username" />
        <Input aria-label="Password" type="password" value={p} onChange={(e) => setP(e.target.value)} placeholder="Password (min 8)" />
        {tooShort && <p role="alert" className="text-sm text-danger">Password must be at least 8 characters</p>}
        {setup.isError && <p role="alert" className="text-sm text-danger">Setup failed</p>}
        <Button type="submit" disabled={setup.isPending || tooShort} className="w-full">Create admin</Button>
      </form>
    </div>
  );
}
