import { createRouter, createWebHistory } from 'vue-router'
import { useSession } from './stores/session'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', component: () => import('./views/LoginView.vue'), meta: { public: true } },
    { path: '/', component: () => import('./views/HomeView.vue') },
    { path: '/todos', component: () => import('./views/TodosView.vue') },
    { path: '/teams', component: () => import('./views/TeamsView.vue') },
    { path: '/teams/:id', component: () => import('./views/TeamView.vue'), props: true },
    { path: '/apps', component: () => import('./views/AppsView.vue') },
    { path: '/apps/:app', component: () => import('./views/AppView.vue'), props: true },
    {
      path: '/apps/:app/issues/:number',
      component: () => import('./views/IssueView.vue'),
      props: (r) => ({ app: r.params.app, number: Number(r.params.number) }),
    },
    {
      path: '/apps/:app/repos/:repo/:mode(tree|blob)?/:ref?/:path(.*)*',
      component: () => import('./views/RepoView.vue'),
      props: (r) => ({
        app: r.params.app,
        repo: r.params.repo,
        mode: r.params.mode || 'tree',
        gitRef: r.params.ref || '',
        path: Array.isArray(r.params.path) ? r.params.path.join('/') : r.params.path || '',
      }),
    },
    { path: '/channels/:id', component: () => import('./views/ChannelView.vue'), props: true },
    { path: '/:pathMatch(.*)*', component: () => import('./views/NotFoundView.vue') },
  ],
})

router.beforeEach(async (to) => {
  const session = useSession()
  if (!session.loaded) await session.load()
  if (!to.meta.public && !session.user) return { path: '/login', query: { next: to.fullPath } }
})
