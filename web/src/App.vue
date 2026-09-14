<template>
  <div class="min-h-screen bg-slate-950 text-slate-100 flex flex-col font-sans">
    <header class="bg-slate-900 border-b border-slate-800 h-14 flex items-center justify-between px-4 sticky top-0 z-30">
      <div class="flex items-center space-x-3">
        <div class="w-8 h-8 rounded-lg bg-gradient-to-tr from-sky-600 to-indigo-500 flex items-center justify-center font-bold text-white shadow">
          MV
        </div>
        <span class="font-bold text-lg tracking-wide text-white">MediaVault Console</span>
      </div>
    </header>
    <main class="flex-1 p-6">
      <div class="max-w-4xl mx-auto bg-slate-900 border border-slate-800 rounded-xl p-8 shadow">
        <h2 class="text-xl font-bold text-white mb-2">MediaVault 控制台 (Vue 3 / Vite)</h2>
        <p class="text-slate-400 text-sm">单文件生产产物已内嵌在 <code>web/dist/index.html</code> 并随 Go 二进制文件一同编译分发。</p>
      </div>
    </main>
  </div>
</template>

<script setup>
// Application root component
</script>
