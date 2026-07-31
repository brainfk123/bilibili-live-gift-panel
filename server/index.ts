import html from '../dist/index.html' with { type: 'text' };
import { getRoomInfo } from './bili-api';

const PORT = Number(process.env.PORT ?? 12450);

const server = Bun.serve({
  port: PORT,
  async fetch(req) {
    const url = new URL(req.url);

    if (url.pathname === '/' || url.pathname === '/index.html') {
      return new Response(html, { headers: { 'Content-Type': 'text/html; charset=utf-8' } });
    }

    if (url.pathname === '/api/room_info') {
      const roomId = url.searchParams.get('roomId');
      if (!roomId) return Response.json({ code: -1, message: '缺少房间号' });
      try {
        const info = await getRoomInfo(roomId);
        return Response.json({ code: 0, ...info });
      } catch (e) {
        return Response.json({ code: -1, message: (e as Error).message });
      }
    }

    return new Response('not found', { status: 404 });
  },
});

console.log(`Bilibili live gift panel: http://localhost:${server.port}/?mode=config`);
console.log(`OBS 浏览器源请加载: http://localhost:${server.port}/?mode=display`);
