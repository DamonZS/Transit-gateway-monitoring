"use client"

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react"
import {
  apiFetch,
  setToken,
  setUnauthorizedHandler,
} from "@/lib/api"

type AuthStatus = "loading" | "anonymous" | "authenticated"

interface AuthContextValue {
  status: AuthStatus
  username: string | null
  /** 后端关闭了鉴权（AUTH_ENABLED=false），整套 UI 当作"已登录"渲染。 */
  authDisabled: boolean
  login: (username: string, password: string) => Promise<void>
  logout: () => void
}

const AuthContext = createContext<AuthContextValue | null>(null)

interface LoginResponse {
  token?: string
  expires_at?: number
  username: string
  auth_disabled?: boolean
}

interface MeResponse {
  username: string
  auth_disabled?: boolean
}

interface SSOPublicConfig {
  enabled: boolean
  parent_origin?: string
}

interface ParentSSOResult {
  login: LoginResponse
  nonce: string
  parentOrigin: string
  targetWindow: Window
}

const EMBEDDED_SSO_TIMEOUT_MS = 12_000

type SSOTarget = {
  window: Window
}

function createSSONonce(): string {
  const bytes = new Uint8Array(16)
  window.crypto.getRandomValues(bytes)
  return Array.from(bytes, (value) => value.toString(16).padStart(2, "0")).join("")
}

function getSSOTarget(): SSOTarget | null {
  // Preserve the explicit iframe opt-in. A standalone window opened from
  // Toporeduce instead relies on window.opener, which is unavailable when a
  // user navigates to UpstreamOps directly.
  if (
    window.parent !== window &&
    new URLSearchParams(window.location.search).get("embed") === "1"
  ) {
    return { window: window.parent }
  }

  if (window.opener && !window.opener.closed) {
    return { window: window.opener }
  }

  return null
}

async function tryParentSSO(parentSignal: AbortSignal): Promise<ParentSSOResult | null> {
  const target = getSSOTarget()
  if (!target || parentSignal.aborted) return null

  const controller = new AbortController()
  const abort = () => controller.abort()
  parentSignal.addEventListener("abort", abort, { once: true })
  const timeout = window.setTimeout(abort, EMBEDDED_SSO_TIMEOUT_MS)

  try {
    const config = await apiFetch<SSOPublicConfig>("/auth/sso/config", {
      signal: controller.signal,
      skipAuthErrorHandler: true,
    })
    if (!config.enabled || !config.parent_origin || controller.signal.aborted) return null

    let parentOrigin: string
    try {
      parentOrigin = new URL(config.parent_origin).origin
    } catch {
      return null
    }
    if (parentOrigin !== config.parent_origin) return null

    const requestedParent = new URLSearchParams(window.location.search).get("parent_origin")
    if (requestedParent) {
      try {
        if (new URL(requestedParent).origin !== parentOrigin) return null
      } catch {
        return null
      }
    }

    const nonce = createSSONonce()
    return await new Promise<ParentSSOResult | null>((resolve) => {
      let settled = false
      let exchanging = false

      const finish = (result: ParentSSOResult | null) => {
        if (settled) return
        settled = true
        window.removeEventListener("message", onMessage)
        controller.signal.removeEventListener("abort", onAbort)
        resolve(result)
      }
      const onAbort = () => finish(null)
      const onMessage = async (event: MessageEvent) => {
        if (exchanging || event.source !== target.window || event.origin !== parentOrigin) return
        if (!event.data || typeof event.data !== "object") return
        const message = event.data as {
          type?: unknown
          payload?: { nonce?: unknown; assertion?: unknown }
        }
        if (
          message.type === "toporeduce:sso-unavailable" &&
          message.payload?.nonce === nonce
        ) {
          finish(null)
          return
        }
        if (message.type !== "toporeduce:sso-assertion") return
        if (message.payload?.nonce !== nonce || typeof message.payload.assertion !== "string") return

        exchanging = true
        try {
          const login = await apiFetch<LoginResponse>("/auth/sso/exchange", {
            method: "POST",
            body: JSON.stringify({ assertion: message.payload.assertion, nonce }),
            signal: controller.signal,
            skipAuthErrorHandler: true,
          })
          if (!login.token) throw new Error("SSO exchange returned no token")
          finish({ login, nonce, parentOrigin, targetWindow: target.window })
        } catch {
          if (!controller.signal.aborted) {
            target.window.postMessage(
              { type: "upstream-ops:sso-error", payload: { nonce } },
              parentOrigin,
            )
          }
          finish(null)
        }
      }

      controller.signal.addEventListener("abort", onAbort, { once: true })
      window.addEventListener("message", onMessage)
      target.window.postMessage(
        { type: "upstream-ops:sso-ready", payload: { nonce } },
        parentOrigin,
      )
    })
  } catch {
    return null
  } finally {
    window.clearTimeout(timeout)
    parentSignal.removeEventListener("abort", abort)
  }
}

export function AuthProvider({ children }: { children: ReactNode }) {
  // 启动时无论有没有 token 都先 /auth/me 探测一次，因为后端可能开了"无鉴权模式"。
  const [status, setStatus] = useState<AuthStatus>("loading")
  const [username, setUsername] = useState<string | null>(null)
  const [authDisabled, setAuthDisabled] = useState(false)

  useEffect(() => {
    let cancelled = false
    const controller = new AbortController()

    const bootstrap = async () => {
      try {
        const me = await apiFetch<MeResponse>("/auth/me", {
          signal: controller.signal,
          skipAuthErrorHandler: true,
        })
        if (cancelled) return
        if (me.auth_disabled) {
          // 后端关了鉴权：清掉本地任何遗留 token，避免下次开启时困惑
          setToken(null)
          setAuthDisabled(true)
          setUsername(me.username)
          setStatus("authenticated")
          return
        }
        // 后端开启鉴权：me 成功说明现有 token 仍有效
        setUsername(me.username)
        setStatus("authenticated")
      } catch {
        if (cancelled) return
        // me 失败：清理无效 token，再尝试 Toporeduce iframe/opener SSO。
        setToken(null)
        const sso = await tryParentSSO(controller.signal)
        if (cancelled) return
        if (sso?.login.token) {
          setToken(sso.login.token)
          setAuthDisabled(false)
          setUsername(sso.login.username)
          setStatus("authenticated")
          sso.targetWindow.postMessage(
            { type: "upstream-ops:sso-complete", payload: { nonce: sso.nonce } },
            sso.parentOrigin,
          )
          return
        }
        setUsername(null)
        setStatus("anonymous")
      }
    }

    void bootstrap()
    return () => {
      cancelled = true
      controller.abort()
    }
  }, [])

  // 注册全局 401 回调：让 apiFetch 在任何业务请求 401 时把我们打回登录页。
  // 鉴权关闭时不可能拿到 401，这里也无害。
  useEffect(() => {
    setUnauthorizedHandler(() => {
      setUsername(null)
      setStatus("anonymous")
    })
    return () => setUnauthorizedHandler(null)
  }, [])

  const login = useCallback(async (u: string, p: string) => {
    const res = await apiFetch<LoginResponse>("/auth/login", {
      method: "POST",
      body: JSON.stringify({ username: u, password: p }),
      skipAuthErrorHandler: true,
    })
    if (res.token) {
      setToken(res.token)
    }
    if (res.auth_disabled) {
      setAuthDisabled(true)
    }
    setUsername(res.username)
    setStatus("authenticated")
  }, [])

  const logout = useCallback(() => {
    // 鉴权关闭时 logout 按钮在 UI 上不会展示，这里仍保留兜底逻辑
    apiFetch("/auth/logout", { method: "POST" }).catch(() => {})
    setToken(null)
    setUsername(null)
    setStatus("anonymous")
  }, [])

  const value = useMemo(
    () => ({ status, username, authDisabled, login, logout }),
    [status, username, authDisabled, login, logout],
  )
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) {
    throw new Error("useAuth must be used within AuthProvider")
  }
  return ctx
}
