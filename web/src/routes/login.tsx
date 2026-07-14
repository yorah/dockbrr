import { useState } from "react";
import { useLogin } from "@/hooks/mutations";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

export function LoginScreen() {
  const login = useLogin();
  const [u, setU] = useState("");
  const [p, setP] = useState("");
  return (
    <div className="mx-auto mt-24 max-w-sm space-y-4">
      <h1 className="text-xl font-semibold">Sign in</h1>
      <form
        className="space-y-3"
        onSubmit={(e) => { e.preventDefault(); if (u && p) login.mutate({ username: u, password: p }); }}
      >
        <Input aria-label="Username" value={u} onChange={(e) => setU(e.target.value)} placeholder="Username" />
        <Input aria-label="Password" type="password" value={p} onChange={(e) => setP(e.target.value)} placeholder="Password" />
        {login.isError && <p role="alert" className="text-sm text-danger">Invalid username or password</p>}
        <Button type="submit" disabled={login.isPending || !u || !p} className="w-full">Sign in</Button>
      </form>
    </div>
  );
}
