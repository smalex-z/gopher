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
    return Promise.reject(err)
  }
)

export default client
