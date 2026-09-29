import { FormEvent, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { api } from '../../api/client'
import { Button, Field } from '../../components/Ui'

export function Login() {
  const [username, setUsername] = useState(''); const [password, setPassword] = useState(''); const [error, setError] = useState(''); const [loading, setLoading] = useState(false)
  const queryClient = useQueryClient(); const navigate = useNavigate()
  const submit = async (event: FormEvent) => { event.preventDefault(); setError(''); setLoading(true); try { await api.login(username, password); await queryClient.invalidateQueries({ queryKey: ['me'] }); navigate('/', { replace: true }) } catch (err) { setError(err instanceof Error ? err.message : '登录失败，请稍后重试') } finally { setLoading(false) } }
  return <div className="login-page"><div className="login-layout"><form className="login-form" onSubmit={submit}><h2>管理员登录</h2>{error ? <div className="notice danger login-error">{error}</div> : null}<Field label="账号"><input className="input" autoComplete="username" value={username} onChange={e => setUsername(e.target.value)} placeholder="输入管理员账号" required /></Field><Field label="密码"><input className="input" autoComplete="current-password" type="password" value={password} onChange={e => setPassword(e.target.value)} placeholder="输入管理员密码" required /></Field><Button type="submit" loading={loading}>进入控制台</Button></form></div></div>
}
