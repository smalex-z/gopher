import axios from 'axios'

const client = axios.create({
  baseURL: '/api',
  headers: {
    'Content-Type': 'application/json',
  },
  withCredentials: true,
})

client.interceptors.response.use(
  res => res,
  err => {
    if (err.response?.status === 401) {
      const url: string = err.config?.url ?? ''
      // Step-up re-auth endpoints (download / delete a private key) return 401
      // on a wrong TOTP/password — that's a challenge failure, NOT session
      // expiry. Redirecting to /login there would swallow the "verification
      // failed" message the modal wants to show AND reload away any banner the
      // user was acting on. So only bounce to login for genuine auth failures.
      const isStepUpChallenge = /\/ssh-keys\/[^/]+\/(download|delete-private)$/.test(url)
      if (!url.includes('/auth/') && !isStepUpChallenge) {
        window.location.href = '/login'
      }
    }
    // Surface the server's structured error in err.message so every caller
    // that does `toast.error(e.message)` gets the real reason instead of
    // axios's generic "Request failed with status code 400". The original
    // axios message stays available on err.config / err.code for callers
    // that want it.
    const apiMessage = err.response?.data?.error
    if (typeof apiMessage === 'string' && apiMessage.length > 0) {
      err.message = apiMessage
    }
    return Promise.reject(err)
  }
)

export default client
