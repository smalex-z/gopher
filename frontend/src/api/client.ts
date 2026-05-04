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
      // Avoid redirect loop on auth endpoints themselves
      const url: string = err.config?.url ?? ''
      if (!url.includes('/auth/')) {
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
