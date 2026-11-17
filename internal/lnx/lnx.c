// 桌面工具箱 Linux 原生层实现（GTK3 + Xlib + XTest）。说明见 lnx.h。
#include "lnx.h"

#include <gdk/gdkx.h>
#include <X11/Xutil.h>
#include <X11/keysym.h>
#include <X11/extensions/XTest.h>
#include <poll.h>
#include <string.h>
#include <stdlib.h>
#include <math.h>
#include <stdio.h>

#include "_cgo_export.h"

G_GNUC_BEGIN_IGNORE_DEPRECATIONS

// 在 Go 运行时与 GTK 初始化之前启用 Xlib 多线程支持（热键线程使用独立连接）。
__attribute__((constructor)) static void lnx_xinit_threads(void) { XInitThreads(); }

// ------------------------------------------------------------------ 主线程调度

static gboolean main_call_cb(gpointer data) {
  goMainCall((uintptr_t)data);
  return G_SOURCE_REMOVE;
}

void lnx_call_main(uintptr_t id) { g_idle_add_full(G_PRIORITY_HIGH_IDLE, main_call_cb, (gpointer)id, NULL); }

int lnx_gtk_init(void) { return gtk_init_check(NULL, NULL) ? 1 : 0; }
void lnx_gtk_main(void) { gtk_main(); }
void lnx_gtk_quit(void) { gtk_main_quit(); }
void lnx_free(void *p) { g_free(p); }

static int png_of(GdkPixbuf *pb, unsigned char **out, int *len) {
  gchar *buf = NULL;
  gsize n = 0;
  GError *err = NULL;
  if (!gdk_pixbuf_save_to_buffer(pb, &buf, &n, "png", &err, "compression", "6", NULL)) {
    if (err) g_error_free(err);
    return 0;
  }
  *out = (unsigned char *)buf;
  *len = (int)n;
  return 1;
}

static GdkPixbuf *pixbuf_of_png(const unsigned char *data, int len) {
  GdkPixbufLoader *ld = gdk_pixbuf_loader_new_with_type("png", NULL);
  if (!ld) return NULL;
  GdkPixbuf *pb = NULL;
  if (gdk_pixbuf_loader_write(ld, data, (gsize)len, NULL) && gdk_pixbuf_loader_close(ld, NULL)) {
    pb = gdk_pixbuf_loader_get_pixbuf(ld);
    if (pb) g_object_ref(pb);
  } else {
    gdk_pixbuf_loader_close(ld, NULL);
  }
  g_object_unref(ld);
  return pb;
}

// ------------------------------------------------------------------ 托盘

static GtkStatusIcon *tray;
static GtkWidget *tray_menu;

static void tray_item_cb(GtkMenuItem *mi, gpointer id) { goTrayCmd(GPOINTER_TO_INT(id)); }
static void tray_activate_cb(GtkStatusIcon *s, gpointer u) { goTrayActivate(); }
static void tray_popup_cb(GtkStatusIcon *s, guint button, guint t, gpointer u) {
  gtk_menu_popup(GTK_MENU(tray_menu), NULL, NULL, gtk_status_icon_position_menu, s, button, t);
}

void lnx_tray_begin(const char *tooltip, const char *icon_path) {
  lnx_tray_remove();
  if (icon_path && *icon_path && g_file_test(icon_path, G_FILE_TEST_EXISTS))
    tray = gtk_status_icon_new_from_file(icon_path);
  else
    tray = gtk_status_icon_new_from_icon_name("desktop-toolbox");
  gtk_status_icon_set_tooltip_text(tray, tooltip);
  gtk_status_icon_set_title(tray, tooltip);
  gtk_status_icon_set_name(tray, "desktop-toolbox");
  tray_menu = gtk_menu_new();
  g_signal_connect(tray, "activate", G_CALLBACK(tray_activate_cb), NULL);
  g_signal_connect(tray, "popup-menu", G_CALLBACK(tray_popup_cb), NULL);
}

void lnx_tray_item(int id, const char *label) {
  if (!tray_menu) return;
  GtkWidget *mi;
  if (id == 0) {
    mi = gtk_separator_menu_item_new();
  } else {
    mi = gtk_menu_item_new_with_label(label);
    g_signal_connect(mi, "activate", G_CALLBACK(tray_item_cb), GINT_TO_POINTER(id));
  }
  gtk_menu_shell_append(GTK_MENU_SHELL(tray_menu), mi);
}

void lnx_tray_end(void) {
  if (!tray) return;
  gtk_widget_show_all(tray_menu);
  gtk_status_icon_set_visible(tray, TRUE);
}

void lnx_tray_remove(void) {
  if (tray) {
    gtk_status_icon_set_visible(tray, FALSE);
    g_object_unref(tray);
    tray = NULL;
  }
  if (tray_menu) {
    gtk_widget_destroy(tray_menu);
    tray_menu = NULL;
  }
}

int lnx_tray_embedded(void) { return tray && gtk_status_icon_is_embedded(tray) ? 1 : 0; }

// ------------------------------------------------------------------ 剪贴板

static gboolean clip_watching;
static gulong clip_handler;

static void clip_on_text(GtkClipboard *cb, const gchar *text, gpointer u) {
  if (text && *text) goClipText((char *)text);
}

static void clip_on_image(GtkClipboard *cb, GdkPixbuf *pb, gpointer u) {
  if (!pb) return;
  unsigned char *buf = NULL;
  int n = 0;
  if (png_of(pb, &buf, &n)) {
    goClipPNG(buf, n);
    g_free(buf);
  }
}

static gboolean clip_request(gpointer tries);

static void clip_on_targets(GtkClipboard *cb, GdkAtom *atoms, gint n, gpointer tries) {
  if (!atoms || n <= 0) {
    // 刚取得所有权的程序有时还来不及应答，稍后重试（最多 3 次）
    int t = GPOINTER_TO_INT(tries);
    if (t < 3 && clip_watching && !lnx_clip_owned()) g_timeout_add(80 * (t + 1), clip_request, GINT_TO_POINTER(t + 1));
    return;
  }
  if (gtk_targets_include_image(atoms, n, FALSE))
    gtk_clipboard_request_image(cb, clip_on_image, NULL);
  else if (gtk_targets_include_text(atoms, n))
    gtk_clipboard_request_text(cb, clip_on_text, NULL);
}

static gboolean clip_request(gpointer tries) {
  if (clip_watching && !lnx_clip_owned())
    gtk_clipboard_request_targets(gtk_clipboard_get(GDK_SELECTION_CLIPBOARD), clip_on_targets, tries);
  return G_SOURCE_REMOVE;
}

// 剪贴板当前是否由本进程持有（本程序自己写入的内容不记录）。
// 注意：GTK3 的 gdk_selection_owner_get 对其他程序的窗口也会返回（外部窗口包装），不能用它判断。
int lnx_clip_owned(void) {
  GdkDisplay *d = gdk_display_get_default();
  if (!d || !GDK_IS_X11_DISPLAY(d)) return 0;
  Window xw = XGetSelectionOwner(GDK_DISPLAY_XDISPLAY(d), gdk_x11_get_xatom_by_name_for_display(d, "CLIPBOARD"));
  if (xw == None) return 0;
  GdkWindow *w = gdk_x11_window_lookup_for_display(d, xw);
  return w && gdk_window_get_window_type(w) != GDK_WINDOW_FOREIGN ? 1 : 0;
}

static void clip_owner_change(GtkClipboard *cb, GdkEvent *ev, gpointer u) {
  if (!clip_watching) return;
  if (lnx_clip_owned()) return;
  g_timeout_add(30, clip_request, GINT_TO_POINTER(0));
}

void lnx_clip_watch(int on) {
  GtkClipboard *cb = gtk_clipboard_get(GDK_SELECTION_CLIPBOARD);
  if (!clip_handler) clip_handler = g_signal_connect(cb, "owner-change", G_CALLBACK(clip_owner_change), NULL);
  clip_watching = on ? TRUE : FALSE;
}

void lnx_clip_set_text(const char *text) {
  GtkClipboard *cb = gtk_clipboard_get(GDK_SELECTION_CLIPBOARD);
  gtk_clipboard_set_text(cb, text, -1);
}

int lnx_clip_set_png(const unsigned char *data, int len) {
  GdkPixbuf *pb = pixbuf_of_png(data, len);
  if (!pb) return 0;
  gtk_clipboard_set_image(gtk_clipboard_get(GDK_SELECTION_CLIPBOARD), pb);
  g_object_unref(pb);
  return 1;
}

char *lnx_clip_get_text(void) { return gtk_clipboard_wait_for_text(gtk_clipboard_get(GDK_SELECTION_CLIPBOARD)); }

int lnx_clip_get_png(unsigned char **out, int *len) {
  GdkPixbuf *pb = gtk_clipboard_wait_for_image(gtk_clipboard_get(GDK_SELECTION_CLIPBOARD));
  if (!pb) return 0;
  int ok = png_of(pb, out, len);
  g_object_unref(pb);
  return ok;
}

// ------------------------------------------------------------------ 主窗口

static GtkWindow *find_window(const char *title) {
  GtkWindow *found = NULL;
  GList *l = gtk_window_list_toplevels();
  for (GList *it = l; it; it = it->next) {
    GtkWindow *w = GTK_WINDOW(it->data);
    if (gtk_window_get_window_type(w) != GTK_WINDOW_TOPLEVEL) continue;
    const char *t = gtk_window_get_title(w);
    if (t && strcmp(t, title) == 0) {
      found = w;
      break;
    }
  }
  g_list_free(l);
  return found;
}

int lnx_win_state(const char *title) {
  GtkWindow *w = find_window(title);
  if (!w) return 0;
  int st = 0;
  if (gtk_widget_get_visible(GTK_WIDGET(w))) {
    GdkWindow *gw = gtk_widget_get_window(GTK_WIDGET(w));
    if (!gw || !(gdk_window_get_state(gw) & (GDK_WINDOW_STATE_ICONIFIED | GDK_WINDOW_STATE_WITHDRAWN))) st |= 1;
  }
  if (gtk_window_is_active(w)) st |= 2;
  return st;
}

// 发送 _NET_ACTIVE_WINDOW（来源=2“分页器”，可绕过窗口管理器的防抢焦点策略）。
static void net_activate(GdkWindow *gw, guint32 t) {
  GdkDisplay *gd = gdk_window_get_display(gw);
  if (!GDK_IS_X11_DISPLAY(gd)) return;
  Display *d = GDK_DISPLAY_XDISPLAY(gd);
  XEvent ev;
  memset(&ev, 0, sizeof(ev));
  ev.xclient.type = ClientMessage;
  ev.xclient.window = GDK_WINDOW_XID(gw);
  ev.xclient.message_type = XInternAtom(d, "_NET_ACTIVE_WINDOW", False);
  ev.xclient.format = 32;
  ev.xclient.data.l[0] = 2;
  ev.xclient.data.l[1] = t;
  XSendEvent(d, DefaultRootWindow(d), False, SubstructureRedirectMask | SubstructureNotifyMask, &ev);
  XFlush(d);
}

void lnx_win_present(const char *title) {
  GtkWindow *w = find_window(title);
  if (!w) return;
  gtk_widget_show(GTK_WIDGET(w));
  GdkWindow *gw = gtk_widget_get_window(GTK_WIDGET(w));
  guint32 t = GDK_CURRENT_TIME;
  if (gw && GDK_IS_X11_WINDOW(gw)) {
    t = gdk_x11_get_server_time(gw);
    gdk_x11_window_set_user_time(gw, t);
  }
  gtk_window_deiconify(w);
  gtk_window_present_with_time(w, t);
  if (gw && GDK_IS_X11_WINDOW(gw)) net_activate(gw, t);
}

void lnx_win_set_icon(const char *title, const char *icon_path) {
  GtkWindow *w = find_window(title);
  if (w && icon_path && g_file_test(icon_path, G_FILE_TEST_EXISTS)) gtk_window_set_icon_from_file(w, icon_path, NULL);
}

// ------------------------------------------------------------------ 截屏

static GdkPixbuf *capture_root(double *scale) {
  GdkWindow *root = gdk_get_default_root_window();
  int w = gdk_window_get_width(root), h = gdk_window_get_height(root);
  GdkPixbuf *pb = gdk_pixbuf_get_from_window(root, 0, 0, w, h);
  if (pb && scale) *scale = w > 0 ? (double)gdk_pixbuf_get_width(pb) / w : 1.0;
  return pb;
}

int lnx_capture_png(unsigned char **out, int *len, int *w, int *h) {
  GdkPixbuf *pb = capture_root(NULL);
  if (!pb) return 0;
  *w = gdk_pixbuf_get_width(pb);
  *h = gdk_pixbuf_get_height(pb);
  int ok = png_of(pb, out, len);
  g_object_unref(pb);
  return ok;
}

void lnx_screen_info(int *pw, int *ph, double *scale) {
  GdkWindow *root = gdk_get_default_root_window();
  int s = gdk_window_get_scale_factor(root);
  if (s < 1) s = 1;
  *scale = s;
  *pw = gdk_window_get_width(root) * s;
  *ph = gdk_window_get_height(root) * s;
}

// 鼠标所在显示器（物理像素）。
void lnx_monitor_at_pointer(int *x, int *y, int *w, int *h) {
  GdkDisplay *d = gdk_display_get_default();
  GdkSeat *seat = gdk_display_get_default_seat(d);
  int px = 0, py = 0;
  gdk_device_get_position(gdk_seat_get_pointer(seat), NULL, &px, &py);
  GdkMonitor *m = gdk_display_get_monitor_at_point(d, px, py);
  GdkWindow *root = gdk_get_default_root_window();
  int s = gdk_window_get_scale_factor(root);
  if (s < 1) s = 1;
  if (!m) {
    *x = 0;
    *y = 0;
    *w = gdk_window_get_width(root) * s;
    *h = gdk_window_get_height(root) * s;
    return;
  }
  GdkRectangle g;
  gdk_monitor_get_geometry(m, &g);
  *x = g.x * s;
  *y = g.y * s;
  *w = g.width * s;
  *h = g.height * s;
}

// ------------------------------------------------------------------ 文本绘制

static void text_size(cairo_t *cr, const char *s, int bold, double pt, double *w, double *h) {
  PangoLayout *pl = pango_cairo_create_layout(cr);
  PangoFontDescription *fd = pango_font_description_from_string(bold ? "Sans Bold" : "Sans");
  pango_font_description_set_size(fd, (int)(pt * PANGO_SCALE));
  pango_layout_set_font_description(pl, fd);
  pango_layout_set_text(pl, s, -1);
  int iw, ih;
  pango_layout_get_pixel_size(pl, &iw, &ih);
  *w = iw;
  *h = ih;
  pango_font_description_free(fd);
  g_object_unref(pl);
}

static void draw_text(cairo_t *cr, const char *s, double cx, double cy, int bold, double pt, double r, double g, double b) {
  PangoLayout *pl = pango_cairo_create_layout(cr);
  PangoFontDescription *fd = pango_font_description_from_string(bold ? "Sans Bold" : "Sans");
  pango_font_description_set_size(fd, (int)(pt * PANGO_SCALE));
  pango_layout_set_font_description(pl, fd);
  pango_layout_set_text(pl, s, -1);
  int iw, ih;
  pango_layout_get_pixel_size(pl, &iw, &ih);
  cairo_set_source_rgb(cr, r, g, b);
  cairo_move_to(cr, cx - iw / 2.0, cy - ih / 2.0);
  pango_cairo_show_layout(cr, pl);
  pango_font_description_free(fd);
  g_object_unref(pl);
}

static void rounded(cairo_t *cr, double x, double y, double w, double h, double r) {
  cairo_new_sub_path(cr);
  cairo_arc(cr, x + w - r, y + r, r, -M_PI / 2, 0);
  cairo_arc(cr, x + w - r, y + h - r, r, 0, M_PI / 2);
  cairo_arc(cr, x + r, y + h - r, r, M_PI / 2, M_PI);
  cairo_arc(cr, x + r, y + r, r, M_PI, 3 * M_PI / 2);
  cairo_close_path(cr);
}

// ------------------------------------------------------------------ 框选遮罩

typedef struct {
  const char *label;
  int action;
  int primary, danger;
  double x, y, w, h;
} OvBtn;

static struct {
  GtkWidget *win;
  GdkPixbuf *shot;
  cairo_surface_t *surf;
  double scale; // 物理像素 / 逻辑像素
  int lw, lh;   // 逻辑尺寸
  int mode, fixw, fixh;
  int state; // 0 未选择 1 拖拽 2 已选择 3 移动
  double sx, sy, x0, y0, x1, y1, mdx, mdy;
  int nb, hover, grabbed, tries;
  OvBtn btn[6];
  uint64_t token;
} ov;

static int ov_has_sel(void) { return ov.state >= 1 && ov.x1 - ov.x0 >= 1 && ov.y1 - ov.y0 >= 1; }

static void ov_layout_buttons(cairo_t *cr) {
  if (ov.mode == OVM_SHOT) {
    static const OvBtn b[] = {{"取消", OV_CANCEL, 0, 0}, {"保存", OV_SAVE, 0, 0},       {"存桌面", OV_DESKTOP, 0, 0},
                              {"识字/翻译/改写", OV_OCR, 0, 0}, {"复制", OV_COPY, 1, 0}};
    ov.nb = 5;
    memcpy(ov.btn, b, sizeof(b));
  } else {
    static const OvBtn b[] = {{"取消", OV_CANCEL, 0, 0}, {"开始录制", OV_RECORD, 1, 1}};
    ov.nb = 2;
    memcpy(ov.btn, b, sizeof(b));
  }
  const double pad = 6, gap = 6, bh = 30;
  double tw = pad, th = bh + pad * 2;
  for (int i = 0; i < ov.nb; i++) {
    double w, h;
    text_size(cr, ov.btn[i].label, ov.btn[i].primary, 10, &w, &h);
    ov.btn[i].w = w + 24;
    ov.btn[i].h = bh;
    tw += ov.btn[i].w + (i ? gap : 0);
  }
  tw += pad;
  double tx = ov.x1 - tw, ty = ov.y1 + 8;
  if (ty + th > ov.lh) ty = ov.y0 - th - 8;
  if (ty < 0) ty = ov.y1 - th - 8;
  if (ty < 0) ty = 0;
  if (tx < 0) tx = 0;
  if (tx + tw > ov.lw) tx = ov.lw - tw;
  double x = tx + pad;
  for (int i = 0; i < ov.nb; i++) {
    ov.btn[i].x = x;
    ov.btn[i].y = ty + pad;
    x += ov.btn[i].w + gap;
  }
  // 工具栏背景
  cairo_set_source_rgba(cr, 1, 1, 1, 0.97);
  rounded(cr, tx, ty, tw, th, 8);
  cairo_fill(cr);
}

static gboolean ov_draw(GtkWidget *w, cairo_t *cr, gpointer u) {
  cairo_set_source_surface(cr, ov.surf, 0, 0);
  cairo_paint(cr);
  cairo_set_source_rgba(cr, 0, 0, 0, 0.45);
  cairo_set_fill_rule(cr, CAIRO_FILL_RULE_EVEN_ODD);
  cairo_rectangle(cr, 0, 0, ov.lw, ov.lh);
  int sel = ov_has_sel();
  if (sel) cairo_rectangle(cr, ov.x0, ov.y0, ov.x1 - ov.x0, ov.y1 - ov.y0);
  cairo_fill(cr);
  cairo_set_fill_rule(cr, CAIRO_FILL_RULE_WINDING);
  if (sel) {
    cairo_set_source_rgb(cr, 0.12, 0.53, 0.98);
    cairo_set_line_width(cr, 2);
    cairo_rectangle(cr, ov.x0 - 1, ov.y0 - 1, ov.x1 - ov.x0 + 2, ov.y1 - ov.y0 + 2);
    cairo_stroke(cr);
    char buf[64];
    snprintf(buf, sizeof buf, "%d × %d", (int)lround((ov.x1 - ov.x0) * ov.scale), (int)lround((ov.y1 - ov.y0) * ov.scale));
    double tw, th;
    text_size(cr, buf, 0, 9, &tw, &th);
    double lx = ov.x0, ly = ov.y0 - th - 10;
    if (ly < 0) ly = ov.y0 + 4;
    cairo_set_source_rgba(cr, 0.1, 0.1, 0.12, 0.85);
    rounded(cr, lx, ly, tw + 12, th + 6, 4);
    cairo_fill(cr);
    draw_text(cr, buf, lx + (tw + 12) / 2, ly + (th + 6) / 2, 0, 9, 1, 1, 1);
  }
  if (ov.state == 2 && ov.mode != OVM_RECORD_FIXED) {
    ov_layout_buttons(cr);
    for (int i = 0; i < ov.nb; i++) {
      OvBtn *b = &ov.btn[i];
      if (b->primary)
        cairo_set_source_rgb(cr, b->danger ? 0.86 : 0.12, b->danger ? 0.2 : 0.47, b->danger ? 0.2 : 0.95);
      else
        cairo_set_source_rgb(cr, ov.hover == i ? 0.9 : 0.95, ov.hover == i ? 0.92 : 0.95, ov.hover == i ? 0.96 : 0.97);
      rounded(cr, b->x, b->y, b->w, b->h, 6);
      cairo_fill(cr);
      if (b->primary)
        draw_text(cr, b->label, b->x + b->w / 2, b->y + b->h / 2, 1, 10, 1, 1, 1);
      else
        draw_text(cr, b->label, b->x + b->w / 2, b->y + b->h / 2, 0, 10, 0.15, 0.15, 0.18);
    }
  }
  if (ov.state == 0 || ov.mode == OVM_RECORD_FIXED) {
    const char *hint = ov.mode == OVM_SHOT     ? "拖动鼠标选择截图区域 · 回车或双击复制 · Esc 取消"
                       : ov.mode == OVM_RECORD ? "拖动鼠标选择录制区域 · Esc 取消"
                                               : "移动鼠标放置录制框，单击开始录制 · Esc 取消";
    double tw, th;
    text_size(cr, hint, 0, 11, &tw, &th);
    GdkRectangle g = {0, 0, ov.lw, ov.lh};
    GdkDisplay *d = gdk_display_get_default();
    int px = 0, py = 0;
    gdk_device_get_position(gdk_seat_get_pointer(gdk_display_get_default_seat(d)), NULL, &px, &py);
    GdkMonitor *m = gdk_display_get_monitor_at_point(d, px, py);
    if (m) gdk_monitor_get_geometry(m, &g);
    double bx = g.x + (g.width - tw - 28) / 2, by = g.y + 40;
    cairo_set_source_rgba(cr, 0.1, 0.1, 0.12, 0.85);
    rounded(cr, bx, by, tw + 28, th + 16, 8);
    cairo_fill(cr);
    draw_text(cr, hint, bx + (tw + 28) / 2, by + (th + 16) / 2, 0, 11, 1, 1, 1);
  }
  return TRUE;
}

static void ov_finish(int action) {
  if (!ov.win) return;
  GdkDisplay *d = gtk_widget_get_display(ov.win);
  if (ov.grabbed) gdk_seat_ungrab(gdk_display_get_default_seat(d));
  gtk_widget_destroy(ov.win);
  ov.win = NULL;
  gdk_display_flush(d);
  int pw = gdk_pixbuf_get_width(ov.shot), ph = gdk_pixbuf_get_height(ov.shot);
  int x = (int)lround(ov.x0 * ov.scale), y = (int)lround(ov.y0 * ov.scale);
  int w = (int)lround((ov.x1 - ov.x0) * ov.scale), h = (int)lround((ov.y1 - ov.y0) * ov.scale);
  if (x < 0) x = 0;
  if (y < 0) y = 0;
  if (x + w > pw) w = pw - x;
  if (y + h > ph) h = ph - y;
  if (w < 1 || h < 1) action = OV_CANCEL;
  unsigned char *buf = NULL;
  int n = 0;
  if (action >= OV_COPY && action <= OV_OCR) {
    GdkPixbuf *sub = gdk_pixbuf_new_subpixbuf(ov.shot, x, y, w, h);
    if (!sub || !png_of(sub, &buf, &n)) action = OV_CANCEL;
    if (sub) g_object_unref(sub);
  }
  cairo_surface_destroy(ov.surf);
  g_object_unref(ov.shot);
  ov.surf = NULL;
  ov.shot = NULL;
  goOverlayDone(ov.token, action, buf, n, x, y, w, h);
  if (buf) g_free(buf);
}

static void ov_fixed_at(double mx, double my) {
  double w = ov.fixw / ov.scale, h = ov.fixh / ov.scale;
  double x = mx - w / 2, y = my - h / 2;
  if (x + w > ov.lw) x = ov.lw - w;
  if (y + h > ov.lh) y = ov.lh - h;
  if (x < 0) x = 0;
  if (y < 0) y = 0;
  ov.x0 = x;
  ov.y0 = y;
  ov.x1 = x + w;
  ov.y1 = y + h;
  ov.state = 1;
}

static int ov_hit(double x, double y) {
  if (ov.state != 2) return -1;
  for (int i = 0; i < ov.nb; i++) {
    OvBtn *b = &ov.btn[i];
    if (x >= b->x && x <= b->x + b->w && y >= b->y && y <= b->y + b->h) return i;
  }
  return -1;
}

static int ov_primary(void) { return ov.mode == OVM_SHOT ? OV_COPY : OV_RECORD; }

static gboolean ov_press(GtkWidget *w, GdkEventButton *e, gpointer u) {
  if (e->button == 3) {
    ov_finish(OV_CANCEL);
    return TRUE;
  }
  if (e->button != 1) return TRUE;
  if (ov.mode == OVM_RECORD_FIXED) {
    ov_fixed_at(e->x, e->y);
    ov_finish(OV_RECORD);
    return TRUE;
  }
  int inside = ov_has_sel() && e->x >= ov.x0 && e->x <= ov.x1 && e->y >= ov.y0 && e->y <= ov.y1;
  if (e->type == GDK_2BUTTON_PRESS) {
    if (inside) ov_finish(ov_primary());
    return TRUE;
  }
  if (e->type != GDK_BUTTON_PRESS) return TRUE;
  int hit = ov_hit(e->x, e->y);
  if (hit >= 0) {
    ov_finish(ov.btn[hit].action);
    return TRUE;
  }
  if (ov.state == 2 && inside) {
    ov.state = 3;
    ov.mdx = e->x - ov.x0;
    ov.mdy = e->y - ov.y0;
    return TRUE;
  }
  ov.state = 1;
  ov.sx = ov.x0 = ov.x1 = e->x;
  ov.sy = ov.y0 = ov.y1 = e->y;
  gtk_widget_queue_draw(w);
  return TRUE;
}

static gboolean ov_motion(GtkWidget *w, GdkEventMotion *e, gpointer u) {
  if (ov.mode == OVM_RECORD_FIXED) {
    ov_fixed_at(e->x, e->y);
  } else if (ov.state == 1) {
    ov.x0 = MIN(ov.sx, e->x);
    ov.x1 = MAX(ov.sx, e->x);
    ov.y0 = MIN(ov.sy, e->y);
    ov.y1 = MAX(ov.sy, e->y);
  } else if (ov.state == 3) {
    double sw = ov.x1 - ov.x0, sh = ov.y1 - ov.y0;
    double x = e->x - ov.mdx, y = e->y - ov.mdy;
    if (x < 0) x = 0;
    if (y < 0) y = 0;
    if (x + sw > ov.lw) x = ov.lw - sw;
    if (y + sh > ov.lh) y = ov.lh - sh;
    ov.x0 = x;
    ov.y0 = y;
    ov.x1 = x + sw;
    ov.y1 = y + sh;
  } else if (ov.state == 2) {
    int h = ov_hit(e->x, e->y);
    if (h == ov.hover) return TRUE;
    ov.hover = h;
  } else if (ov.state == 0) {
    return TRUE;
  }
  gtk_widget_queue_draw(w);
  return TRUE;
}

static gboolean ov_release(GtkWidget *w, GdkEventButton *e, gpointer u) {
  if (e->button != 1) return TRUE;
  if (ov.state == 1) {
    if ((ov.x1 - ov.x0) * ov.scale < 4 || (ov.y1 - ov.y0) * ov.scale < 4)
      ov.state = 0;
    else
      ov.state = 2;
  } else if (ov.state == 3) {
    ov.state = 2;
  }
  ov.hover = -1;
  gtk_widget_queue_draw(w);
  return TRUE;
}

static gboolean ov_key(GtkWidget *w, GdkEventKey *e, gpointer u) {
  if (e->keyval == GDK_KEY_Escape) {
    ov_finish(OV_CANCEL);
  } else if ((e->keyval == GDK_KEY_Return || e->keyval == GDK_KEY_KP_Enter) && ov.state == 2) {
    ov_finish(ov_primary());
  }
  return TRUE;
}

static gboolean ov_try_grab(gpointer u) {
  if (!ov.win) return G_SOURCE_REMOVE;
  GdkWindow *gw = gtk_widget_get_window(ov.win);
  if (!gw) return G_SOURCE_CONTINUE;
  GdkDisplay *d = gtk_widget_get_display(ov.win);
  GdkCursor *cur = gdk_cursor_new_from_name(d, "crosshair");
  GdkGrabStatus st = gdk_seat_grab(gdk_display_get_default_seat(d), gw, GDK_SEAT_CAPABILITY_ALL, TRUE, cur, NULL, NULL, NULL);
  if (cur) g_object_unref(cur);
  if (st == GDK_GRAB_SUCCESS) {
    ov.grabbed = 1;
    return G_SOURCE_REMOVE;
  }
  // 触发热键的按键还没松开时键盘处于被动抓取状态，稍后重试（最多约 5 秒）
  if (++ov.tries > 100) {
    // 无法抓取键盘：仍可用鼠标操作，右键取消
    GdkGrabStatus s2 = gdk_seat_grab(gdk_display_get_default_seat(d), gw, GDK_SEAT_CAPABILITY_ALL_POINTING, TRUE, NULL, NULL, NULL, NULL);
    ov.grabbed = s2 == GDK_GRAB_SUCCESS;
    return G_SOURCE_REMOVE;
  }
  return G_SOURCE_CONTINUE;
}

static gboolean ov_map(GtkWidget *w, GdkEvent *e, gpointer u) {
  if (ov_try_grab(NULL) == G_SOURCE_CONTINUE) g_timeout_add(50, ov_try_grab, NULL);
  return FALSE;
}

int lnx_overlay_active(void) { return ov.win != NULL; }

int lnx_overlay_show(int mode, int fixw, int fixh, uint64_t token) {
  if (ov.win) return 0;
  double scale = 1;
  GdkPixbuf *pb = capture_root(&scale);
  if (!pb) return 0;
  GdkWindow *root = gdk_get_default_root_window();
  memset(&ov, 0, sizeof ov);
  ov.shot = pb;
  ov.scale = scale > 0 ? scale : 1;
  ov.lw = gdk_window_get_width(root);
  ov.lh = gdk_window_get_height(root);
  ov.mode = mode;
  ov.fixw = fixw;
  ov.fixh = fixh;
  ov.hover = -1;
  ov.token = token;
  ov.surf = gdk_cairo_surface_create_from_pixbuf(pb, (int)lround(ov.scale), NULL);
  GtkWidget *w = gtk_window_new(GTK_WINDOW_POPUP);
  ov.win = w;
  gtk_window_set_title(GTK_WINDOW(w), mode == OVM_SHOT ? "桌面工具箱-截图" : "桌面工具箱-录屏区域");
  gtk_widget_set_app_paintable(w, TRUE);
  gtk_window_move(GTK_WINDOW(w), 0, 0);
  gtk_window_set_default_size(GTK_WINDOW(w), ov.lw, ov.lh);
  gtk_widget_set_size_request(w, ov.lw, ov.lh);
  gtk_widget_add_events(w, GDK_BUTTON_PRESS_MASK | GDK_BUTTON_RELEASE_MASK | GDK_POINTER_MOTION_MASK | GDK_KEY_PRESS_MASK);
  g_signal_connect(w, "draw", G_CALLBACK(ov_draw), NULL);
  g_signal_connect(w, "button-press-event", G_CALLBACK(ov_press), NULL);
  g_signal_connect(w, "button-release-event", G_CALLBACK(ov_release), NULL);
  g_signal_connect(w, "motion-notify-event", G_CALLBACK(ov_motion), NULL);
  g_signal_connect(w, "key-press-event", G_CALLBACK(ov_key), NULL);
  g_signal_connect(w, "map-event", G_CALLBACK(ov_map), NULL);
  if (mode == OVM_RECORD_FIXED) {
    int px = 0, py = 0;
    gdk_device_get_position(gdk_seat_get_pointer(gdk_display_get_default_seat(gdk_display_get_default())), NULL, &px, &py);
    ov_fixed_at(px, py);
  }
  gtk_widget_show(w);
  return 1;
}

// ------------------------------------------------------------------ 录屏悬浮条

static GtkWidget *rb_win, *rb_label;
static guint rb_timer;
static gint64 rb_start;

static gboolean rb_tick(gpointer u) {
  if (!rb_label) return G_SOURCE_REMOVE;
  gint64 s = (g_get_real_time() / 1000 - rb_start) / 1000;
  if (s < 0) s = 0;
  char buf[96];
  if (s >= 3600)
    snprintf(buf, sizeof buf, "<span foreground='#e5484d'>●</span> 录制中 %d:%02d:%02d", (int)(s / 3600), (int)(s / 60 % 60), (int)(s % 60));
  else
    snprintf(buf, sizeof buf, "<span foreground='#e5484d'>●</span> 录制中 %02d:%02d", (int)(s / 60), (int)(s % 60));
  gtk_label_set_markup(GTK_LABEL(rb_label), buf);
  return G_SOURCE_CONTINUE;
}

static void rb_stop_cb(GtkButton *b, gpointer u) { goRecbarStop(); }

void lnx_recbar_hide(void) {
  if (rb_timer) {
    g_source_remove(rb_timer);
    rb_timer = 0;
  }
  if (rb_win) {
    gtk_widget_destroy(rb_win);
    rb_win = NULL;
    rb_label = NULL;
  }
}

void lnx_recbar_show(int x, int y, int w, int h, int64_t start_ms) {
  lnx_recbar_hide();
  rb_start = start_ms;
  GtkWidget *win = gtk_window_new(GTK_WINDOW_TOPLEVEL);
  rb_win = win;
  gtk_window_set_title(GTK_WINDOW(win), "桌面工具箱-录屏");
  gtk_window_set_decorated(GTK_WINDOW(win), FALSE);
  gtk_window_set_keep_above(GTK_WINDOW(win), TRUE);
  gtk_window_set_skip_taskbar_hint(GTK_WINDOW(win), TRUE);
  gtk_window_set_skip_pager_hint(GTK_WINDOW(win), TRUE);
  gtk_window_set_accept_focus(GTK_WINDOW(win), FALSE);
  gtk_window_set_type_hint(GTK_WINDOW(win), GDK_WINDOW_TYPE_HINT_UTILITY);
  gtk_window_stick(GTK_WINDOW(win));
  GtkWidget *box = gtk_box_new(GTK_ORIENTATION_HORIZONTAL, 10);
  gtk_container_set_border_width(GTK_CONTAINER(box), 8);
  rb_label = gtk_label_new("");
  GtkWidget *btn = gtk_button_new_with_label("停止录制");
  g_signal_connect(btn, "clicked", G_CALLBACK(rb_stop_cb), NULL);
  gtk_box_pack_start(GTK_BOX(box), rb_label, FALSE, FALSE, 0);
  gtk_box_pack_start(GTK_BOX(box), btn, FALSE, FALSE, 0);
  gtk_container_add(GTK_CONTAINER(win), box);
  rb_tick(NULL);
  gtk_widget_show_all(win);
  // 放在录制区域外：下方 → 上方 → 区域内右上角（逻辑坐标）
  GdkWindow *root = gdk_get_default_root_window();
  int s = gdk_window_get_scale_factor(root);
  if (s < 1) s = 1;
  int lw = gdk_window_get_width(root), lh = gdk_window_get_height(root);
  int rx = x / s, ry = y / s, rw = w / s, rh = h / s;
  int bw, bh;
  gtk_window_get_size(GTK_WINDOW(win), &bw, &bh);
  int bx = rx + (rw - bw) / 2, by;
  if (ry + rh + 8 + bh <= lh)
    by = ry + rh + 8;
  else if (ry - 8 - bh >= 0)
    by = ry - 8 - bh;
  else {
    by = ry + 8;
    bx = rx + rw - bw - 8;
  }
  if (bx < 0) bx = 0;
  if (bx + bw > lw) bx = lw - bw;
  gtk_window_move(GTK_WINDOW(win), bx, by);
  rb_timer = g_timeout_add(500, rb_tick, NULL);
}

// ------------------------------------------------------------------ 杂项

static void alert_response(GtkDialog *d, gint r, gpointer u) { gtk_widget_destroy(GTK_WIDGET(d)); }

void lnx_alert(const char *title, const char *msg) {
  GtkWidget *d = gtk_message_dialog_new(NULL, 0, GTK_MESSAGE_WARNING, GTK_BUTTONS_OK, "%s", msg);
  gtk_window_set_title(GTK_WINDOW(d), title);
  gtk_window_set_keep_above(GTK_WINDOW(d), TRUE);
  gtk_window_set_position(GTK_WINDOW(d), GTK_WIN_POS_CENTER);
  g_signal_connect(d, "response", G_CALLBACK(alert_response), NULL);
  gtk_widget_show_all(d);
  gtk_window_present(GTK_WINDOW(d));
}

int lnx_show_uri(const char *uri, char **err) {
  GError *e = NULL;
  if (gtk_show_uri_on_window(NULL, uri, GDK_CURRENT_TIME, &e)) return 1;
  if (e) {
    *err = g_strdup(e->message);
    g_error_free(e);
  }
  return 0;
}

static GtkWidget *test_win;

static gboolean test_draw(GtkWidget *w, cairo_t *cr, gpointer u) {
  int W = gtk_widget_get_allocated_width(w), H = gtk_widget_get_allocated_height(w);
  cairo_pattern_t *p = cairo_pattern_create_linear(0, 0, W, H);
  cairo_pattern_add_color_stop_rgb(p, 0, 0.9, 0.2, 0.2);
  cairo_pattern_add_color_stop_rgb(p, 0.5, 0.2, 0.8, 0.3);
  cairo_pattern_add_color_stop_rgb(p, 1, 0.2, 0.3, 0.9);
  cairo_set_source(cr, p);
  cairo_paint(cr);
  cairo_pattern_destroy(p);
  draw_text(cr, "桌面工具箱 自检", W / 2.0, H / 2.0, 1, 20, 1, 1, 1);
  return TRUE;
}

void lnx_test_window(int show) {
  if (!show) {
    if (test_win) gtk_widget_destroy(test_win);
    test_win = NULL;
    return;
  }
  if (test_win) return;
  test_win = gtk_window_new(GTK_WINDOW_TOPLEVEL);
  gtk_window_set_title(GTK_WINDOW(test_win), "桌面工具箱-自检");
  gtk_window_set_decorated(GTK_WINDOW(test_win), FALSE);
  gtk_window_set_default_size(GTK_WINDOW(test_win), 480, 320);
  gtk_window_move(GTK_WINDOW(test_win), 40, 40);
  gtk_widget_set_app_paintable(test_win, TRUE);
  g_signal_connect(test_win, "draw", G_CALLBACK(test_draw), NULL);
  gtk_widget_show_all(test_win);
}

// ------------------------------------------------------------------ 全局热键（独立 X 连接，XGrabKey）

static Display *hk_err_dpy;
static int hk_err_code;
static XErrorHandler hk_prev_handler;

static int hk_error_handler(Display *d, XErrorEvent *e) {
  if (d == hk_err_dpy) {
    hk_err_code = e->error_code;
    return 0;
  }
  return hk_prev_handler ? hk_prev_handler(d, e) : 0;
}

Display *lnx_hk_open(void) { return XOpenDisplay(NULL); }

unsigned int lnx_hk_keycode(Display *d, unsigned long keysym) { return XKeysymToKeycode(d, (KeySym)keysym); }

int lnx_hk_grab(Display *d, unsigned int keycode, unsigned int mods, int grab) {
  if (!keycode) return 0;
  Window root = DefaultRootWindow(d);
  unsigned int extra[] = {0, LockMask, Mod2Mask, LockMask | Mod2Mask};
  XSync(d, False);
  hk_err_dpy = d;
  hk_err_code = 0;
  hk_prev_handler = XSetErrorHandler(hk_error_handler);
  for (int i = 0; i < 4; i++) {
    if (grab)
      XGrabKey(d, keycode, mods | extra[i], root, True, GrabModeAsync, GrabModeAsync);
    else
      XUngrabKey(d, keycode, mods | extra[i], root);
  }
  XSync(d, False);
  int failed = grab && hk_err_code != 0;
  if (failed) {
    for (int i = 0; i < 4; i++) XUngrabKey(d, keycode, mods | extra[i], root);
    XSync(d, False);
  }
  XSetErrorHandler(hk_prev_handler);
  hk_err_dpy = NULL;
  return failed ? 0 : 1;
}

int lnx_hk_poll(Display *d, int timeout_ms, unsigned int *keycode, unsigned int *state) {
  if (!XPending(d)) {
    struct pollfd p = {ConnectionNumber(d), POLLIN, 0};
    if (poll(&p, 1, timeout_ms) <= 0) return 0;
  }
  while (XPending(d)) {
    XEvent ev;
    XNextEvent(d, &ev);
    if (ev.type == KeyPress) {
      *keycode = ev.xkey.keycode;
      *state = ev.xkey.state & (ShiftMask | ControlMask | Mod1Mask | Mod4Mask);
      return 1;
    }
  }
  return 0;
}

// ------------------------------------------------------------------ XTest

int lnx_xtest_keys(const unsigned long *keysyms, int n) {
  Display *d = XOpenDisplay(NULL);
  if (!d) return 0;
  int ev, er, mj, mn;
  if (!XTestQueryExtension(d, &ev, &er, &mj, &mn)) {
    XCloseDisplay(d);
    return 0;
  }
  for (int i = 0; i < n; i++) XTestFakeKeyEvent(d, XKeysymToKeycode(d, keysyms[i]), True, 0);
  for (int i = n - 1; i >= 0; i--) XTestFakeKeyEvent(d, XKeysymToKeycode(d, keysyms[i]), False, 10);
  XSync(d, False);
  XCloseDisplay(d);
  return 1;
}

int lnx_xtest_pointer(int x, int y, int button, int press) {
  Display *d = XOpenDisplay(NULL);
  if (!d) return 0;
  XTestFakeMotionEvent(d, -1, x, y, 0);
  if (button > 0) XTestFakeButtonEvent(d, button, press ? True : False, 0);
  XSync(d, False);
  XCloseDisplay(d);
  return 1;
}

G_GNUC_END_IGNORE_DEPRECATIONS
