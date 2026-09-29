import { ReactNode } from 'react'
import { AlertCircle, Check, LoaderCircle, RefreshCw } from 'lucide-react'

export function Button({ children, variant = 'primary', loading, className = '', ...props }: React.ButtonHTMLAttributes<HTMLButtonElement> & { variant?: 'primary' | 'secondary' | 'danger' | 'ghost'; loading?: boolean }) {
  return <button className={`btn btn-${variant} ${className}`} disabled={props.disabled || loading} {...props}>{loading ? <LoaderCircle className="spin" size={16} /> : null}{children}</button>
}

export function Card({ children, className = '', ...props }: { children: ReactNode; className?: string } & React.HTMLAttributes<HTMLElement>) {
  return <section className={`card ${className}`} {...props}>{children}</section>
}

export function PageHeader({ eyebrow, title, description, action }: { eyebrow?: string; title: string; description?: string; action?: ReactNode }) {
  return <div className="page-header"><div><div className="eyebrow">{eyebrow || 'GM CONTROL PLANE'}</div><h1>{title}</h1>{description ? <p>{description}</p> : null}</div>{action}</div>
}

export function StatusPill({ status, children }: { status: string; children?: ReactNode }) {
  const tone = ['up', 'online', 'completed', 'published', 'ok'].includes(status) ? 'success' : ['pending', 'accepted', 'draft'].includes(status) ? 'warning' : ['down', 'rejected', 'error'].includes(status) ? 'danger' : 'neutral'
  return <span className={`status-pill ${tone}`}><i />{children || status}</span>
}

export function EmptyState({ message = '暂无数据' }: { message?: string }) { return <div className="empty"><AlertCircle size={18} /><span>{message}</span></div> }
export function LoadingState() { return <div className="loading-state"><LoaderCircle className="spin" size={20} />加载中…</div> }
export function ErrorState({ message = '服务暂时不可用', onRetry }: { message?: string; onRetry?: () => void }) { return <div className="error-state"><AlertCircle size={20} /><div><strong>读取失败</strong><p>{message}</p>{onRetry ? <Button variant="secondary" onClick={onRetry}><RefreshCw size={15} />重试</Button> : null}</div></div> }
export function Field({ label, children, hint }: { label: string; children: ReactNode; hint?: string }) { return <label className="field"><span>{label}</span>{children}{hint ? <small>{hint}</small> : null}</label> }
export function CheckMark() { return <Check size={15} /> }
