import {
  createContext,
  useCallback,
  useContext,
  useState,
  type ReactNode,
} from "react";
import { CheckIcon, CopyIcon, DownloadIcon } from "lucide-react";
import { toast } from "sonner";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { message } from "@/api/session";

export type Secret = {
  title: string;
  description: string;
  token: string;
  filename: string;
};
type Confirmation = {
  title: string;
  description: string;
  action: string;
  destructive?: boolean;
  perform: () => Promise<unknown>;
  done?: () => void;
};
type Feedback = {
  confirm: (c: Confirmation) => void;
  showSecret: (s: Secret) => void;
};
const FeedbackContext = createContext<Feedback | null>(null);

export function FeedbackProvider({ children }: { children: ReactNode }) {
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null);
  const [secret, setSecret] = useState<Secret | null>(null);
  const confirm = useCallback((c: Confirmation) => setConfirmation(c), []);
  const showSecret = useCallback((s: Secret) => setSecret(s), []);
  return (
    <FeedbackContext.Provider value={{ confirm, showSecret }}>
      {children}
      {confirmation && (
        <ConfirmDialog
          value={confirmation}
          onClose={() => setConfirmation(null)}
        />
      )}
      {secret && (
        <SecretDialog secret={secret} onClose={() => setSecret(null)} />
      )}
    </FeedbackContext.Provider>
  );
}

export function useFeedback() {
  const value = useContext(FeedbackContext);
  if (!value) throw new Error("feedback context missing");
  return value;
}

function ConfirmDialog({
  value,
  onClose,
}: {
  value: Confirmation;
  onClose: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return (
    <AlertDialog open onOpenChange={(open) => !open && !busy && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{value.title}</AlertDialogTitle>
          <AlertDialogDescription>{value.description}</AlertDialogDescription>
        </AlertDialogHeader>
        {error && (
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>取消</AlertDialogCancel>
          <Button
            variant={value.destructive ? "destructive" : "default"}
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              setError("");
              try {
                await value.perform();
                toast.success("操作已完成");
                value.done?.();
                onClose();
              } catch (e) {
                setError(message(e));
                setBusy(false);
              }
            }}
          >
            {busy && <Spinner />}
            {value.action}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

function SecretDialog({
  secret,
  onClose,
}: {
  secret: Secret;
  onClose: () => void;
}) {
  const [copied, setCopied] = useState(false);
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{secret.title}</DialogTitle>
          <DialogDescription>{secret.description}</DialogDescription>
        </DialogHeader>
        <Alert>
          <AlertDescription>
            完整令牌只显示这一次，关闭后无法再次查看。请妥善保存，仅交给指定使用者。
          </AlertDescription>
        </Alert>
        <Textarea
          readOnly
          aria-label="一次性显示的令牌"
          value={secret.token}
          className="font-mono text-xs"
          rows={3}
        />
        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => {
              const url = URL.createObjectURL(
                new Blob([secret.token + "\n"], {
                  type: "text/plain;charset=utf-8",
                }),
              );
              const link = document.createElement("a");
              link.href = url;
              link.download = secret.filename;
              link.click();
              setTimeout(() => URL.revokeObjectURL(url), 1000);
            }}
          >
            <DownloadIcon />
            下载
          </Button>
          <Button
            variant="outline"
            onClick={async () => {
              try {
                await navigator.clipboard.writeText(secret.token);
                setCopied(true);
              } catch {
                toast.error("无法自动复制，请手动选中令牌复制。");
              }
            }}
          >
            {copied ? <CheckIcon /> : <CopyIcon />}
            {copied ? "已复制" : "复制"}
          </Button>
          <Button onClick={onClose}>已保存</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
