use eframe::egui;
use serde::Deserialize;
use std::sync::mpsc::{channel, Receiver, Sender};

const BASE_URL: &str = "http://127.0.0.1:8080";
const PAGE_SIZE: usize = 1000; // How many images per page fetch
const PRELOAD_ROWS: usize = 2; // Rows ahead to preload thumbnails

// ═══════════════════════════════════════════════════════════════════
//  THEME: Fotoro Dark — slate base, warm amber accent
// ═══════════════════════════════════════════════════════════════════

fn setup_theme(ctx: &egui::Context) {
    let mut visuals = egui::Visuals::dark();

    // Core palette
    // bg-900: #13151a  bg-800: #1c1f26  bg-700: #252830  bg-600: #2e3240
    // surface: #1c1f26  border: #2e3240  text-primary: #e8eaf0  text-muted: #7b8096
    // accent: #f5a623 (warm amber)

    visuals.panel_fill         = egui::Color32::from_rgb(19, 21, 26);     // bg-900
    visuals.extreme_bg_color   = egui::Color32::from_rgb(28, 31, 38);     // bg-800
    visuals.code_bg_color      = egui::Color32::from_rgb(37, 40, 48);     // bg-700
    visuals.faint_bg_color     = egui::Color32::from_rgb(28, 31, 38);

    visuals.override_text_color = Some(egui::Color32::from_rgb(232, 234, 240));

    visuals.selection.bg_fill  = egui::Color32::from_rgba_premultiplied(245, 166, 35, 50);
    visuals.selection.stroke   = egui::Stroke::new(1.0, egui::Color32::from_rgb(245, 166, 35));

    visuals.hyperlink_color    = egui::Color32::from_rgb(245, 166, 35);

    // Widgets
    visuals.widgets.noninteractive.bg_fill    = egui::Color32::from_rgb(28, 31, 38);
    visuals.widgets.noninteractive.bg_stroke  = egui::Stroke::new(1.0, egui::Color32::from_rgb(46, 50, 64));
    visuals.widgets.noninteractive.fg_stroke  = egui::Stroke::new(1.0, egui::Color32::from_rgb(123, 128, 150));

    visuals.widgets.inactive.bg_fill    = egui::Color32::from_rgb(37, 40, 48);
    visuals.widgets.inactive.bg_stroke  = egui::Stroke::new(1.0, egui::Color32::from_rgb(46, 50, 64));
    visuals.widgets.inactive.fg_stroke  = egui::Stroke::new(1.0, egui::Color32::from_rgb(232, 234, 240));
    visuals.widgets.inactive.rounding   = egui::Rounding::same(6.0);

    visuals.widgets.hovered.bg_fill   = egui::Color32::from_rgb(46, 50, 64);
    visuals.widgets.hovered.bg_stroke = egui::Stroke::new(1.0, egui::Color32::from_rgb(245, 166, 35));
    visuals.widgets.hovered.fg_stroke = egui::Stroke::new(1.5, egui::Color32::from_rgb(232, 234, 240));
    visuals.widgets.hovered.rounding  = egui::Rounding::same(6.0);

    visuals.widgets.active.bg_fill   = egui::Color32::from_rgb(245, 166, 35);
    visuals.widgets.active.fg_stroke = egui::Stroke::new(1.5, egui::Color32::from_rgb(19, 21, 26));
    visuals.widgets.active.rounding  = egui::Rounding::same(6.0);

    visuals.widgets.open.bg_fill  = egui::Color32::from_rgb(46, 50, 64);
    visuals.widgets.open.rounding = egui::Rounding::same(6.0);

    // Window chrome
    visuals.window_fill         = egui::Color32::from_rgb(28, 31, 38);
    visuals.window_stroke       = egui::Stroke::new(1.0, egui::Color32::from_rgb(46, 50, 64));
    visuals.window_rounding     = egui::Rounding::same(8.0);
    visuals.popup_shadow        = egui::epaint::Shadow::NONE;
    visuals.menu_rounding       = egui::Rounding::same(6.0);
    visuals.button_frame        = true;
    visuals.collapsing_header_frame = false;

    ctx.set_visuals(visuals);

    // Typography — fall back to built-in proportional (no external font file needed)
    // If you have the font, uncomment:
    // let mut fonts = egui::FontDefinitions::default();
    // fonts.font_data.insert("jb_mono".to_owned(),
    //     egui::FontData::from_static(include_bytes!("../fonts/JetBrainsMono-Regular.ttf")));
    // fonts.families.get_mut(&egui::FontFamily::Monospace).unwrap().insert(0, "jb_mono".to_owned());
    // ctx.set_fonts(fonts);

    ctx.style_mut(|style| {
        style.spacing.item_spacing   = egui::vec2(8.0, 8.0);
        style.spacing.button_padding = egui::vec2(12.0, 6.0);
        style.spacing.scroll        = egui::style::ScrollStyle::solid();
        style.spacing.scroll.bar_width  = 6.0;
    });
}

// ═══════════════════════════════════════════════════════════════════
//  API TYPES
// ═══════════════════════════════════════════════════════════════════

#[derive(Deserialize, Clone, Debug)]
struct ApiImage {
    #[serde(default)]
    path: String,
    hash: String,
    caption: String,
    category: String,
    thumbnail: String,
    #[serde(default)]
    score: String,
    #[serde(default)]
    created_at: String,
}

#[derive(Deserialize)]
struct SearchResponse {
    results: Vec<ApiImage>,
}

#[derive(Deserialize)]
struct StatsResponse {
    total: i32,
    processed: i32,
    #[serde(default)]
    failed: i32,
}

// ═══════════════════════════════════════════════════════════════════
//  IMAGE ENTRY  (per-image state)
// ═══════════════════════════════════════════════════════════════════

struct ImageEntry {
    api: ApiImage,
    texture: Option<egui::TextureHandle>,
    aspect: f32,
    /// thumb fetch has been dispatched
    fetch_dispatched: bool,
    /// alpha for fade-in animation 0.0 → 1.0
    fade: f32,
    selected: bool,
}

impl ImageEntry {
    fn from_api(api: ApiImage) -> Self {
        Self { api, texture: None, aspect: 1.0, fetch_dispatched: false, fade: 0.0, selected: false }
    }
}

// ═══════════════════════════════════════════════════════════════════
//  ASYNC MESSAGES
// ═══════════════════════════════════════════════════════════════════

enum Msg {
    Thumbnail(usize, egui::ColorImage, f32 /* aspect */),
    FullImage(egui::ColorImage, f32 /* aspect */),
    AppendImages(Vec<ApiImage>),
    Stats(i32, i32),
}

// ═══════════════════════════════════════════════════════════════════
//  SIDEBAR VIEW MODES
// ═══════════════════════════════════════════════════════════════════

#[derive(Clone, Copy, PartialEq, Debug)]
enum ViewMode {
    All,
    Photos,
    Screenshots,
    Documents,
    Wallpapers,
    People,
    Uncategorized,
}

impl ViewMode {
    fn label(self) -> &'static str {
        match self {
            ViewMode::All          => "All Images",
            ViewMode::Photos       => "Photos",
            ViewMode::Screenshots  => "Screenshots",
            ViewMode::Documents    => "Documents",
            ViewMode::Wallpapers   => "Wallpapers",
            ViewMode::People       => "People",
            ViewMode::Uncategorized => "Uncategorized",
        }
    }

    fn icon(self) -> &'static str {
        match self {
            ViewMode::All          => "◈",
            ViewMode::Photos       => "◻",
            ViewMode::Screenshots  => "▣",
            ViewMode::Documents    => "▤",
            ViewMode::Wallpapers   => "▥",
            ViewMode::People       => "◉",
            ViewMode::Uncategorized => "◌",
        }
    }

    fn category_filter(self) -> Option<&'static str> {
        match self {
            ViewMode::Photos        => Some("photo"),
            ViewMode::Screenshots   => Some("screenshots"),
            ViewMode::Documents     => Some("documents"),
            ViewMode::Wallpapers    => Some("wallpapers"),
            ViewMode::People        => Some("people"),
            ViewMode::Uncategorized => Some("unknown"),
            ViewMode::All           => None,
        }
    }
}

// ═══════════════════════════════════════════════════════════════════
//  APP STATE
// ═══════════════════════════════════════════════════════════════════

struct FotoroApp {
    images: Vec<ImageEntry>,
    query: String,
    search_pending: bool,

    // selection / lightbox
    selected_image: Option<usize>,
    full_image: Option<egui::TextureHandle>,
    full_aspect: f32,
    show_lightbox: bool,
    full_loading: bool,

    // async
    rx: Receiver<Msg>,
    tx: Sender<Msg>,

    // pagination / infinite scroll
    page: usize,
    has_more: bool,
    fetching: bool,
    search_mode: bool,

    // stats
    total_images: i32,

    // UI config
    sidebar_expanded: bool,
    current_view: ViewMode,
    status_message: String,
    grid_zoom: f32,         // thumb target px
}

impl FotoroApp {
    fn new(cc: &eframe::CreationContext<'_>) -> Self {
        setup_theme(&cc.egui_ctx);
        let (tx, rx) = channel();
        let mut app = Self {
            images: vec![],
            query: String::new(),
            search_pending: false,
            selected_image: None,
            full_image: None,
            full_aspect: 1.0,
            show_lightbox: false,
            full_loading: false,
            rx,
            tx,
            page: 1,
            has_more: true,
            fetching: true,
            search_mode: false,
            total_images: 0,
            sidebar_expanded: true,
            current_view: ViewMode::All,
            status_message: "Loading…".into(),
            grid_zoom: 200.0,
        };
        app.fetch_stats();
        app.dispatch_fetch_page(1);
        app
    }

    // ── Fetchers ────────────────────────────────────────────────

    fn fetch_stats(&self) {
        let tx = self.tx.clone();
        std::thread::spawn(move || {
            if let Ok(resp) = reqwest::blocking::get(format!("{}/api/stats", BASE_URL)) {
                if let Ok(s) = resp.json::<StatsResponse>() {
                    let _ = tx.send(Msg::Stats(s.total, s.processed));
                }
            }
        });
    }

    fn dispatch_fetch_page(&mut self, page: usize) {
        if self.fetching { return; }
        self.fetching = true;
        let tx = self.tx.clone();
        let category = self.current_view.category_filter().map(String::from);
        std::thread::spawn(move || {
            let mut url = format!("{}/api/images?page={}&per_page={}&sort=date_desc", BASE_URL, page, PAGE_SIZE);
            if let Some(cat) = category {
                url.push_str(&format!("&category={}", cat));
            }
            match reqwest::blocking::get(&url) {
                Ok(resp) => match resp.text() {
                    Ok(text) => {
                        if let Ok(list) = serde_json::from_str::<Vec<ApiImage>>(&text) {
                            let _ = tx.send(Msg::AppendImages(list));
                        } else {
                            // try search-response envelope
                            if let Ok(sr) = serde_json::from_str::<SearchResponse>(&text) {
                                let _ = tx.send(Msg::AppendImages(sr.results));
                            }
                        }
                    }
                    Err(_) => { let _ = tx.send(Msg::AppendImages(vec![])); }
                },
                Err(_) => { let _ = tx.send(Msg::AppendImages(vec![])); }
            }
        });
    }

    fn search_dispatch(&mut self) {
        self.fetching = true;
        self.search_mode = true;
        let tx = self.tx.clone();
        let q = self.query.clone();
        std::thread::spawn(move || {
            let url = format!("{}/api/search?q={}", BASE_URL, urlencoding::encode(&q));
            match reqwest::blocking::get(&url) {
                Ok(resp) => {
                    let text = resp.text().unwrap_or_default();
                    // Try array first, then envelope
                    let results: Vec<ApiImage> =
                        serde_json::from_str::<Vec<ApiImage>>(&text)
                        .or_else(|_| serde_json::from_str::<SearchResponse>(&text).map(|r| r.results))
                        .unwrap_or_default();
                    let _ = tx.send(Msg::AppendImages(results));
                }
                Err(_) => { let _ = tx.send(Msg::AppendImages(vec![])); }
            }
        });
    }

    fn fetch_thumbnail(&self, idx: usize, thumb_path: &str) {
        let tx = self.tx.clone();
        let url = format!("{}{}", BASE_URL, thumb_path);
        std::thread::spawn(move || {
            if let Ok(resp) = reqwest::blocking::get(&url) {
                if let Ok(bytes) = resp.bytes() {
                    if let Ok(img) = image::load_from_memory(&bytes) {
                        let aspect = img.width() as f32 / img.height().max(1) as f32;
                        let ci = to_color_image(&img.to_rgba8());
                        let _ = tx.send(Msg::Thumbnail(idx, ci, aspect));
                    }
                }
            }
        });
    }

    fn fetch_full_image(&mut self, hash: &str) {
        self.full_loading = true;
        self.full_image = None;
        let tx = self.tx.clone();
        let url = format!("{}/api/image/{}", BASE_URL, hash);
        std::thread::spawn(move || {
            if let Ok(resp) = reqwest::blocking::get(&url) {
                if let Ok(bytes) = resp.bytes() {
                    if let Ok(img) = image::load_from_memory(&bytes) {
                        let aspect = img.width() as f32 / img.height().max(1) as f32;
                        let ci = to_color_image(&img.to_rgba8());
                        let _ = tx.send(Msg::FullImage(ci, aspect));
                    }
                }
            }
        });
    }

    // ── Reset / refresh ─────────────────────────────────────────

    fn reset_and_reload(&mut self) {
        self.images.clear();
        self.page = 1;
        self.has_more = true;
        self.fetching = false;
        self.search_mode = false;
        self.show_lightbox = false;
        self.status_message = "Loading…".into();
        self.dispatch_fetch_page(1);
    }

    // ── Process incoming messages ────────────────────────────────

    fn drain_messages(&mut self, ctx: &egui::Context) {
        while let Ok(msg) = self.rx.try_recv() {
            match msg {
                Msg::Thumbnail(idx, ci, aspect) => {
                    if let Some(e) = self.images.get_mut(idx) {
                        e.texture = Some(ctx.load_texture(
                            format!("th_{}", e.api.hash),
                            ci,
                            egui::TextureOptions::LINEAR,
                        ));
                        e.aspect = aspect;
                        // fade starts at 0; tick() will advance it
                    }
                }
                Msg::FullImage(ci, aspect) => {
                    self.full_image = Some(ctx.load_texture("full_image", ci, egui::TextureOptions::LINEAR));
                    self.full_aspect = aspect;
                    self.full_loading = false;
                }
                Msg::AppendImages(list) => {
                    let got = list.len();
                    let base = self.images.len();
                    for api in list {
                        self.images.push(ImageEntry::from_api(api));
                    }
                    self.fetching = false;
                    self.has_more = got == PAGE_SIZE;
                    if got == 0 {
                        self.status_message = format!("{} images", self.images.len());
                    } else {
                        self.page += 1;
                        self.status_message = format!("{} images", self.images.len());
                    }
                    // kick off thumbnails for newly arrived entries
                    // (actual dispatch happens in image_grid on visibility check)
                    let _ = base; // suppress unused warning
                }
                Msg::Stats(total, _) => {
                    self.total_images = total;
                }
            }
        }
    }

    // ── UI ──────────────────────────────────────────────────────

    fn draw_toolbar(&mut self, ui: &mut egui::Ui) {
        ui.horizontal_centered(|ui| {
            ui.add_space(12.0);

            // Sidebar toggle
            let toggle_icon = if self.sidebar_expanded { "‹" } else { "›" };
            if ui.add(egui::Button::new(
                egui::RichText::new(toggle_icon).size(18.0)
            ).min_size(egui::vec2(32.0, 32.0))).clicked() {
                self.sidebar_expanded = !self.sidebar_expanded;
            }

            ui.add_space(8.0);

            // Wordmark
            ui.label(
                egui::RichText::new("fotoro")
                    .size(17.0)
                    .color(egui::Color32::from_rgb(245, 166, 35))
                    .strong()
            );

            ui.add_space(20.0);

            // Search bar — fills available space
            let search_w = (ui.available_width() - 200.0).max(160.0);
            let resp = ui.add_sized(
                [search_w, 32.0],
                egui::TextEdit::singleline(&mut self.query)
                    .hint_text("Search images…")
                    .frame(true),
            );
            if resp.lost_focus() && ui.input(|i| i.key_pressed(egui::Key::Enter)) {
                if self.query.is_empty() {
                    self.current_view = ViewMode::All;
                    self.reset_and_reload();
                } else {
                    self.images.clear();
                    self.search_dispatch();
                    self.status_message = format!("Searching: {}", self.query);
                }
            }

            ui.with_layout(egui::Layout::right_to_left(egui::Align::Center), |ui| {
                ui.add_space(12.0);

                // Zoom slider
                ui.add(egui::Slider::new(&mut self.grid_zoom, 100.0..=320.0)
                    .show_value(false)
                    .step_by(10.0))
                    .on_hover_text("Thumbnail size");

                ui.label(
                    egui::RichText::new("⊞").size(16.0)
                        .color(egui::Color32::from_rgb(123, 128, 150))
                );
                ui.add_space(8.0);
            });
        });
    }

    fn draw_sidebar(&mut self, ui: &mut egui::Ui) {
        ui.add_space(16.0);

        // Section header
        ui.label(
            egui::RichText::new("  LIBRARY")
                .size(10.0)
                .color(egui::Color32::from_rgb(123, 128, 150))
                .strong()
        );
        ui.add_space(6.0);

        let views = [
            ViewMode::All,
            ViewMode::Photos,
            ViewMode::Screenshots,
            ViewMode::Documents,
            ViewMode::Wallpapers,
            ViewMode::People,
            ViewMode::Uncategorized,
        ];

        for view in views {
            let selected = self.current_view == view;
            let accent = egui::Color32::from_rgb(245, 166, 35);
            let text_color = if selected {
                accent
            } else {
                egui::Color32::from_rgb(180, 183, 194)
            };

            let btn_text = egui::RichText::new(
                format!("  {}  {}", view.icon(), view.label())
            )
            .size(13.0)
            .color(text_color);

            let resp = ui.add_sized(
                [ui.available_width(), 34.0],
                egui::SelectableLabel::new(selected, btn_text),
            );

            // Left accent bar for selected item
            if selected {
                let bar_rect = egui::Rect::from_min_size(
                    resp.rect.left_top(),
                    egui::vec2(3.0, resp.rect.height()),
                );
                ui.painter().rect_filled(bar_rect, 0.0, accent);
            }

            if resp.clicked() && self.current_view != view {
                self.current_view = view;
                self.query.clear();
                self.reset_and_reload();
            }
        }

        ui.add_space(16.0);
        ui.add(egui::Separator::default().horizontal());
        ui.add_space(12.0);

        // Stats block
        ui.label(
            egui::RichText::new("  STATS")
                .size(10.0)
                .color(egui::Color32::from_rgb(123, 128, 150))
                .strong()
        );
        ui.add_space(6.0);

        let stat_color = egui::Color32::from_rgb(180, 183, 194);
        ui.label(egui::RichText::new(format!("  Total   {}", self.total_images)).size(12.0).color(stat_color));
        ui.label(egui::RichText::new(format!("  Shown   {}", self.images.len())).size(12.0).color(stat_color));
    }

    fn draw_image_grid(&mut self, ui: &mut egui::Ui, ctx: &egui::Context) {
        let base_size = self.grid_zoom;
        let avail_w = ui.available_width();
        let gap = 6.0;
        let cols = ((avail_w + gap) / (base_size + gap)).max(1.0) as usize;
        let cell_w = (avail_w - gap * (cols as f32 - 1.0)) / cols as f32;
        let cell_h = cell_w; // square cells; image letterboxed inside

        // Collect indices for rendering in a fixed-size grid
        let count = self.images.len();
        let rows = (count + cols - 1) / cols;

        // We'll track which indices need thumb fetches / which were clicked
        let mut to_fetch: Vec<(usize, String)> = vec![];
        let mut clicked: Option<(usize, String)> = None;

        // Calculate total grid height for scroll area sizing
        let total_h = rows as f32 * (cell_h + gap);

        let _scroll_output = egui::ScrollArea::vertical()
            .auto_shrink([false, false])
            .show(ui, |ui| {
                let start_y = ui.cursor().top();

                // Allocate the full grid space up front (enables proper scrollbar)
                ui.set_min_height(total_h);

                // Viewport in scroll-space
                let vp = ui.clip_rect();

                for row in 0..rows {
                    let row_y = start_y + row as f32 * (cell_h + gap);

                    // Cull entire rows outside viewport + a preload band
                    let preload_band = PRELOAD_ROWS as f32 * (cell_h + gap);
                    if row_y + cell_h < vp.top() - preload_band {
                        continue;
                    }
                    if row_y > vp.bottom() + preload_band {
                        continue;
                    }

                    for col in 0..cols {
                        let idx = row * cols + col;
                        if idx >= count { break; }

                        let x = ui.cursor().left() + col as f32 * (cell_w + gap);
                        let cell_rect = egui::Rect::from_min_size(
                            egui::pos2(x, row_y),
                            egui::vec2(cell_w, cell_h),
                        );

                        let entry = &self.images[idx];

                        // Only actually render if in/near viewport
                        let visible = row_y + cell_h >= vp.top() - 10.0
                            && row_y <= vp.bottom() + 10.0;

                        if visible && !entry.fetch_dispatched {
                            to_fetch.push((idx, entry.api.thumbnail.clone()));
                        }

                        // Draw cell
                        let painter = ui.painter();
                        let selected = entry.selected;

                        // Background
                        let bg = if selected {
                            egui::Color32::from_rgb(60, 52, 32)
                        } else {
                            egui::Color32::from_rgb(28, 31, 38)
                        };
                        painter.rect_filled(cell_rect, 6.0, bg);

                        // Selection ring
                        if selected {
                            painter.rect_stroke(
                                cell_rect.expand(2.0),
                                6.0,
                                egui::Stroke::new(2.0, egui::Color32::from_rgb(245, 166, 35)),
                            );
                        }

                        // Image (if loaded)
                        if let Some(tex) = &entry.texture {
                            let alpha = (entry.fade * 255.0) as u8;
                            let img_rect = fit_rect_cover(cell_rect.shrink(2.0), entry.aspect);
                            painter.image(
                                tex.id(),
                                img_rect,
                                egui::Rect::from_min_max(egui::pos2(0.0, 0.0), egui::pos2(1.0, 1.0)),
                                egui::Color32::from_white_alpha(alpha),
                            );
                        } else if visible {
                            // Skeleton shimmer placeholder
                            let shimmer = egui::Color32::from_rgb(37, 40, 48);
                            painter.rect_filled(cell_rect.shrink(2.0), 4.0, shimmer);
                        }

                        // Click interaction
                        if ui.interact(cell_rect, egui::Id::new(("cell", idx)), egui::Sense::click()).clicked() {
                            clicked = Some((idx, entry.api.hash.clone()));
                        }
                    }
                }

                // Invisible row at bottom to trigger next-page fetch
                if self.has_more && !self.fetching {
                    let bottom_probe = egui::Rect::from_min_size(
                        egui::pos2(0.0, start_y + total_h - cell_h * 2.0),
                        egui::vec2(1.0, 1.0),
                    );
                    if ui.clip_rect().intersects(bottom_probe) {
                        // User has scrolled near the end — fetch next page
                        // We can't call self methods here; set a flag instead
                        ui.ctx().data_mut(|d| d.insert_temp(egui::Id::new("fetch_next"), true));
                    }
                }
            });

        // Check fetch-next flag
        let fetch_next: bool = ctx.data_mut(|d| d.get_temp(egui::Id::new("fetch_next")).unwrap_or(false));
        if fetch_next {
            ctx.data_mut(|d| d.remove_temp::<bool>(egui::Id::new("fetch_next")));
            if self.has_more && !self.fetching {
                self.dispatch_fetch_page(self.page);
            }
        }

        // Dispatch thumbnail fetches
        for (idx, thumb_path) in to_fetch {
            if let Some(e) = self.images.get_mut(idx) {
                e.fetch_dispatched = true;
            }
            self.fetch_thumbnail(idx, &thumb_path);
        }

        // Handle click
        if let Some((idx, hash)) = clicked {
            for e in &mut self.images { e.selected = false; }
            if let Some(e) = self.images.get_mut(idx) { e.selected = true; }
            self.selected_image = Some(idx);
            self.fetch_full_image(&hash);
            self.show_lightbox = true;
        }

        // Advance fade for loaded images (simple CPU-side animation)
        let dt = ctx.input(|i| i.stable_dt).min(0.05);
        let mut needs_repaint = false;
        for e in &mut self.images {
            if e.texture.is_some() && e.fade < 1.0 {
                e.fade = (e.fade + dt * 4.0).min(1.0); // ~0.25s fade
                needs_repaint = true;
            }
        }
        if needs_repaint || self.fetching {
            ctx.request_repaint_after(std::time::Duration::from_millis(16));
        }
    }

    fn draw_lightbox(&mut self, ctx: &egui::Context) {
        if !self.show_lightbox { return; }
        let Some(idx) = self.selected_image else { return };
        let entry = &self.images[idx];
        let entry_caption = entry.api.caption.clone();
        let entry_category = entry.api.category.clone();
        let _entry_path = entry.api.path.clone();
        let _entry_hash = entry.api.hash.clone();
        let total = self.images.len();

        // Close on Escape / Left / Right navigation
        if ctx.input(|i| i.key_pressed(egui::Key::Escape)) {
            self.show_lightbox = false;
            return;
        }
        if ctx.input(|i| i.key_pressed(egui::Key::ArrowLeft)) && idx > 0 {
            for e in &mut self.images { e.selected = false; }
            let new_idx = idx - 1;
            self.images[new_idx].selected = true;
            self.selected_image = Some(new_idx);
            self.fetch_full_image(&self.images[new_idx].api.hash.clone());
        }
        if ctx.input(|i| i.key_pressed(egui::Key::ArrowRight)) && idx + 1 < total {
            for e in &mut self.images { e.selected = false; }
            let new_idx = idx + 1;
            self.images[new_idx].selected = true;
            self.selected_image = Some(new_idx);
            self.fetch_full_image(&self.images[new_idx].api.hash.clone());
        }

        egui::Area::new(egui::Id::new("lightbox"))
            .fixed_pos(egui::pos2(0.0, 0.0))
            .order(egui::Order::Foreground)
            .show(ctx, |ui| {
                let screen = ctx.screen_rect();

                // Dim backdrop — clickable to close
                ui.painter().rect_filled(
                    screen,
                    0.0,
                    egui::Color32::from_rgba_premultiplied(8, 9, 12, 230),
                );
                if ui.interact(screen, egui::Id::new("lb_bg"), egui::Sense::click()).clicked() {
                    self.show_lightbox = false;
                    return;
                }

                // Image area
                let pad = 80.0;
                let max_w = screen.width() - pad * 2.0;
                let max_h = screen.height() - 140.0;

                let (img_w, img_h) = if self.full_aspect >= 1.0 {
                    let w = max_w.min(max_h * self.full_aspect);
                    (w, w / self.full_aspect)
                } else {
                    let h = max_h.min(max_w / self.full_aspect);
                    (h * self.full_aspect, h)
                };

                let img_rect = egui::Rect::from_center_size(
                    screen.center() - egui::vec2(0.0, 30.0),
                    egui::vec2(img_w, img_h),
                );

                if let Some(tex) = &self.full_image {
                    ui.painter().image(
                        tex.id(), img_rect,
                        egui::Rect::from_min_max(egui::pos2(0.0, 0.0), egui::pos2(1.0, 1.0)),
                        egui::Color32::WHITE,
                    );
                } else {
                    // Loading skeleton
                    ui.painter().rect_filled(img_rect, 8.0, egui::Color32::from_rgb(28, 31, 38));
                    ui.painter().text(
                        img_rect.center(), egui::Align2::CENTER_CENTER,
                        "Loading…",
                        egui::FontId::proportional(15.0),
                        egui::Color32::from_rgb(123, 128, 150),
                    );
                }

                // ── Navigation arrows ──
                let arrow_y = screen.center().y - 30.0;

                let left_rect = egui::Rect::from_center_size(
                    egui::pos2(img_rect.left() - 40.0, arrow_y),
                    egui::vec2(36.0, 48.0),
                );
                let right_rect = egui::Rect::from_center_size(
                    egui::pos2(img_rect.right() + 40.0, arrow_y),
                    egui::vec2(36.0, 48.0),
                );

                // Draw arrow buttons
                for (rect, label, enabled) in [
                    (left_rect, "‹", idx > 0),
                    (right_rect, "›", idx + 1 < total),
                ] {
                    let arrow_color = if enabled {
                        egui::Color32::from_rgb(232, 234, 240)
                    } else {
                        egui::Color32::from_rgb(60, 64, 80)
                    };
                    ui.painter().rect_filled(rect, 6.0, egui::Color32::from_rgba_premultiplied(28, 31, 38, 180));
                    ui.painter().text(rect.center(), egui::Align2::CENTER_CENTER, label, egui::FontId::proportional(28.0), arrow_color);
                }

                if enabled_click(ui, left_rect, egui::Id::new("lb_left")) && idx > 0 {
                    for e in &mut self.images { e.selected = false; }
                    let ni = idx - 1;
                    self.images[ni].selected = true;
                    self.selected_image = Some(ni);
                    self.fetch_full_image(&self.images[ni].api.hash.clone());
                }
                if enabled_click(ui, right_rect, egui::Id::new("lb_right")) && idx + 1 < total {
                    for e in &mut self.images { e.selected = false; }
                    let ni = idx + 1;
                    self.images[ni].selected = true;
                    self.selected_image = Some(ni);
                    self.fetch_full_image(&self.images[ni].api.hash.clone());
                }

                // ── Close button ──
                let close_rect = egui::Rect::from_min_size(
                    screen.right_top() + egui::vec2(-52.0, 12.0),
                    egui::vec2(36.0, 36.0),
                );
                ui.painter().rect_filled(close_rect, 6.0, egui::Color32::from_rgba_premultiplied(28, 31, 38, 200));
                ui.painter().text(close_rect.center(), egui::Align2::CENTER_CENTER, "✕", egui::FontId::proportional(16.0), egui::Color32::from_rgb(180, 183, 194));
                if enabled_click(ui, close_rect, egui::Id::new("lb_close")) {
                    self.show_lightbox = false;
                }

                // ── Bottom info bar ──
                let info_y = screen.max.y - 68.0;
                let info_rect = egui::Rect::from_min_size(
                    egui::pos2(screen.min.x + pad, info_y),
                    egui::vec2(screen.width() - pad * 2.0, 56.0),
                );

                ui.painter().rect_filled(info_rect, 8.0, egui::Color32::from_rgba_premultiplied(19, 21, 26, 220));

                // Category chip
                let chip_text = entry_category.to_uppercase();
                ui.painter().text(
                    info_rect.left_center() + egui::vec2(16.0, -8.0),
                    egui::Align2::LEFT_CENTER,
                    &chip_text,
                    egui::FontId::proportional(10.0),
                    egui::Color32::from_rgb(245, 166, 35),
                );
                // Caption
                ui.painter().text(
                    info_rect.left_center() + egui::vec2(16.0, 8.0),
                    egui::Align2::LEFT_CENTER,
                    &entry_caption,
                    egui::FontId::proportional(13.0),
                    egui::Color32::from_rgb(232, 234, 240),
                );

                // Counter top-right of info bar
                ui.painter().text(
                    info_rect.right_center() - egui::vec2(16.0, 0.0),
                    egui::Align2::RIGHT_CENTER,
                    format!("{} / {}", idx + 1, total),
                    egui::FontId::proportional(11.0),
                    egui::Color32::from_rgb(123, 128, 150),
                );
            });
    }

    fn draw_status_bar(&self, ui: &mut egui::Ui) {
        ui.horizontal_centered(|ui| {
            ui.add_space(12.0);
            ui.label(
                egui::RichText::new(&self.status_message)
                    .size(11.0)
                    .color(egui::Color32::from_rgb(123, 128, 150)),
            );
            if self.fetching {
                ui.add_space(8.0);
                ui.spinner();
            }
            ui.with_layout(egui::Layout::right_to_left(egui::Align::Center), |ui| {
                ui.add_space(12.0);
                ui.label(
                    egui::RichText::new(format!(
                        "{}  ·  Page {}",
                        self.current_view.label(),
                        self.page.saturating_sub(1).max(1)
                    ))
                    .size(11.0)
                    .color(egui::Color32::from_rgb(70, 74, 92)),
                );
            });
        });
    }
}

// ═══════════════════════════════════════════════════════════════════
//  eframe App impl
// ═══════════════════════════════════════════════════════════════════

impl eframe::App for FotoroApp {
    fn update(&mut self, ctx: &egui::Context, _frame: &mut eframe::Frame) {
        self.drain_messages(ctx);

        // ── Toolbar ──
        egui::TopBottomPanel::top("toolbar")
            .exact_height(52.0)
            .frame(egui::Frame::none()
                .fill(egui::Color32::from_rgb(19, 21, 26))
                .inner_margin(egui::Margin::symmetric(0.0, 8.0))
                .stroke(egui::Stroke::new(1.0, egui::Color32::from_rgb(37, 40, 48))))
            .show(ctx, |ui| {
                self.draw_toolbar(ui);
            });

        // ── Status bar ──
        egui::TopBottomPanel::bottom("status")
            .exact_height(28.0)
            .frame(egui::Frame::none()
                .fill(egui::Color32::from_rgb(19, 21, 26))
                .inner_margin(egui::Margin::symmetric(0.0, 4.0))
                .stroke(egui::Stroke::new(1.0, egui::Color32::from_rgb(37, 40, 48))))
            .show(ctx, |ui| {
                self.draw_status_bar(ui);
            });

        // ── Sidebar ──
        if self.sidebar_expanded {
            egui::SidePanel::left("sidebar")
                .exact_width(200.0)
                .frame(egui::Frame::none()
                    .fill(egui::Color32::from_rgb(22, 24, 30))
                    .stroke(egui::Stroke::new(1.0, egui::Color32::from_rgb(37, 40, 48))))
                .show(ctx, |ui| {
                    self.draw_sidebar(ui);
                });
        }

        // ── Main grid ──
        egui::CentralPanel::default()
            .frame(egui::Frame::none()
                .fill(egui::Color32::from_rgb(13, 15, 20))
                .inner_margin(egui::Margin::same(10.0)))
            .show(ctx, |ui| {
                if self.images.is_empty() && self.fetching {
                    ui.centered_and_justified(|ui| {
                        ui.spinner();
                    });
                } else if self.images.is_empty() {
                    ui.centered_and_justified(|ui| {
                        ui.label(
                            egui::RichText::new("No images found")
                                .size(15.0)
                                .color(egui::Color32::from_rgb(70, 74, 92)),
                        );
                    });
                } else {
                    self.draw_image_grid(ui, ctx);
                }
            });

        // ── Lightbox (overlay) ──
        if self.show_lightbox {
            self.draw_lightbox(ctx);
            ctx.request_repaint();
        }
    }
}

// ═══════════════════════════════════════════════════════════════════
//  Helpers
// ═══════════════════════════════════════════════════════════════════

/// Letterbox-fit: largest rect with given aspect inside container, centered
fn fit_rect_cover(container: egui::Rect, aspect: f32) -> egui::Rect {
    let cw = container.width();
    let ch = container.height();
    // Cover: fill the container, crop sides
    let container_aspect = cw / ch.max(0.001);
    let (w, h) = if aspect > container_aspect {
        // image is wider → constrain by height
        let h = ch;
        (h * aspect, h)
    } else {
        // image is taller → constrain by width
        let w = cw;
        (w, w / aspect.max(0.001))
    };
    // Crop/clip is handled by painter clipping; center the image
    egui::Rect::from_center_size(container.center(), egui::vec2(w.min(cw), h.min(ch)))
}

fn enabled_click(ui: &egui::Ui, rect: egui::Rect, id: egui::Id) -> bool {
    ui.interact(rect, id, egui::Sense::click()).clicked()
}

fn to_color_image(rgba: &image::RgbaImage) -> egui::ColorImage {
    let (w, h) = rgba.dimensions();
    egui::ColorImage::from_rgba_unmultiplied([w as usize, h as usize], rgba.as_flat_samples().as_slice())
}

// ═══════════════════════════════════════════════════════════════════
//  main
// ═══════════════════════════════════════════════════════════════════

fn main() {
    let options = eframe::NativeOptions {
        viewport: egui::ViewportBuilder::default()
            .with_inner_size([1440.0, 900.0])
            .with_min_inner_size([800.0, 500.0])
            .with_title("Fotoro"),
        ..Default::default()
    };
    eframe::run_native(
        "Fotoro",
        options,
        Box::new(|cc| Ok(Box::new(FotoroApp::new(cc)))),
    ).unwrap();
}
