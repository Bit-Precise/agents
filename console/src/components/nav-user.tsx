import { ChevronsUpDownIcon, LogOutIcon, MoonIcon } from "lucide-react";
import { useTheme } from "@/components/theme-provider";
import { toast } from "sonner";
import { message } from "@/api/session";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from "@/components/ui/sidebar";
import { UserAvatar } from "@/components/cells";
import { useSession, useUser } from "@/lib/session";

export function NavUser() {
  const user = useUser();
  const { signOut } = useSession();
  const { isMobile } = useSidebar();
  const { resolvedTheme, setTheme } = useTheme();
  const subtitle =
    user.email || (user.admin ? "实例管理员" : user.id.slice(0, 8));
  const identity = (
    <>
      <UserAvatar user={user} className="rounded-lg" />
      <div className="grid flex-1 text-left text-sm leading-tight">
        <span className="truncate font-medium">{user.name}</span>
        <span className="truncate text-xs">{subtitle}</span>
      </div>
    </>
  );
  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <SidebarMenuButton
                size="lg"
                aria-label="用户菜单"
                className="aria-expanded:bg-muted"
              />
            }
          >
            {identity}
            <ChevronsUpDownIcon className="ml-auto size-4" />
          </DropdownMenuTrigger>
          <DropdownMenuContent
            className="w-(--anchor-width) min-w-56"
            side={isMobile ? "bottom" : "right"}
            align="end"
            sideOffset={4}
          >
            <DropdownMenuGroup>
              <DropdownMenuLabel className="p-0 font-normal">
                <div className="flex items-center gap-2 px-1 py-1.5 text-left text-sm">
                  {identity}
                </div>
              </DropdownMenuLabel>
            </DropdownMenuGroup>
            <DropdownMenuSeparator />
            <DropdownMenuGroup>
              <DropdownMenuCheckboxItem
                checked={resolvedTheme === "dark"}
                onCheckedChange={(dark) => setTheme(dark ? "dark" : "light")}
              >
                <MoonIcon />
                深色模式
              </DropdownMenuCheckboxItem>
            </DropdownMenuGroup>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              onClick={() => signOut().catch((e) => toast.error(message(e)))}
            >
              <LogOutIcon />
              退出登录
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}
