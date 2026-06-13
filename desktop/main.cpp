#include <QApplication>
#include <QMainWindow>
#include <QWidget>
#include <QHBoxLayout>
#include <QVBoxLayout>
#include <QScrollArea>
#include <QLineEdit>
#include <QSlider>
#include <QLabel>
#include <QPushButton>
#include <QPainter>
#include <QPainterPath>
#include <QNetworkAccessManager>
#include <QNetworkRequest>
#include <QNetworkReply>
#include <QJsonDocument>
#include <QJsonArray>
#include <QJsonObject>
#include <QPixmap>
#include <QScrollBar>
#include <QKeyEvent>
#include <QPropertyAnimation>
#include <QGraphicsOpacityEffect>
#include <QTimer>
#include <QFrame>
#include <QResizeEvent>
#include <QMouseEvent>
#include <QEnterEvent>
#include <QVariantAnimation>
#include <QEasingCurve>
#include <QPointer>
#include <QWindow>
#include <QScreen>
#include <QGuiApplication>
#include <cmath>

// ─── Qt += widgets network in .pro / CMake ────────────────────────
const QString BASE_URL = "http://127.0.0.1:8080";

// ═══════════════════════════════════════════════════════════════════
//  DATA MODEL
// ═══════════════════════════════════════════════════════════════════

struct ImageData {
    QString hash;
    QString caption;
    QString category;
    QString thumbnailPath;
    QPixmap thumbnailPixmap;
    double  score      = 0.0;   // 0 → browse mode, no badge shown
};

// ═══════════════════════════════════════════════════════════════════
//  SHIMMER WIDGET  — animated placeholder while thumbnail loads
// ═══════════════════════════════════════════════════════════════════

class ShimmerWidget : public QWidget {
    Q_OBJECT
public:
    explicit ShimmerWidget(QWidget *parent = nullptr) : QWidget(parent) {
        setAttribute(Qt::WA_OpaquePaintEvent, false);
        _anim = new QVariantAnimation(this);
        _anim->setStartValue(0.0);
        _anim->setEndValue(1.0);
        _anim->setDuration(1500);
        _anim->setLoopCount(-1);
        _anim->setEasingCurve(QEasingCurve::Linear);
        connect(_anim, &QVariantAnimation::valueChanged,
                [this](const QVariant &v){ _pos = v.toReal(); update(); });
        _anim->start();
    }
    void stop() { _anim->stop(); }

protected:
    void paintEvent(QPaintEvent *) override {
        QPainter p(this);
        p.setRenderHint(QPainter::Antialiasing);
        QPainterPath clip;
        clip.addRoundedRect(rect(), 10, 10);
        p.setClipPath(clip);
        p.fillRect(rect(), QColor("#13151c"));
        qreal x = _pos * (width() + 240) - 120;
        QLinearGradient g(x - 80, 0, x + 80, height());
        g.setColorAt(0.0, QColor(255,255,255,0));
        g.setColorAt(0.5, QColor(255,255,255,16));
        g.setColorAt(1.0, QColor(255,255,255,0));
        p.fillRect(rect(), g);
    }
private:
    qreal              _pos = 0.0;
    QVariantAnimation *_anim;
};

// ═══════════════════════════════════════════════════════════════════
//  IMAGE CARD  — widget tile with hover lift animation
// ═══════════════════════════════════════════════════════════════════

class ImageCard : public QFrame {
    Q_OBJECT
    Q_PROPERTY(qreal hover READ hover WRITE setHover)
public:
    static const int W = 200;
    static const int H = 150;

    ImageData data;

    explicit ImageCard(QWidget *parent = nullptr) : QFrame(parent) {
        setAttribute(Qt::WA_Hover);
        setCursor(Qt::PointingHandCursor);
        setFixedSize(W, H);

        _shimmer = new ShimmerWidget(this);
        _shimmer->setGeometry(0, 0, W, H);

        _hoverAnim = new QPropertyAnimation(this, "hover", this);
        _hoverAnim->setDuration(170);
        _hoverAnim->setEasingCurve(QEasingCurve::OutCubic);

        _fadeEff = new QGraphicsOpacityEffect(this);
        _fadeEff->setOpacity(0.0);
        setGraphicsEffect(_fadeEff);
    }

    void setCardData(const ImageData &d) {
        data = d;
        update();
    }

    void setThumbnail(const QPixmap &pix) {
        data.thumbnailPixmap = pix;
        if (_shimmer) { _shimmer->stop(); _shimmer->hide(); }
        auto *a = new QPropertyAnimation(_fadeEff, "opacity", this);
        a->setStartValue(_fadeEff->opacity());
        a->setEndValue(1.0);
        a->setDuration(260);
        a->setEasingCurve(QEasingCurve::OutCubic);
        a->start(QAbstractAnimation::DeleteWhenStopped);
        update();
    }

    // Expose effect so FotoroWindow can set initial opacity for entrance anim
    QGraphicsOpacityEffect *fadeEffect() { return _fadeEff; }

    qreal hover() const { return _hover; }
    void  setHover(qreal v) { _hover = v; update(); }

signals:
    void clicked(const ImageData &data);

protected:
    void enterEvent(QEnterEvent *) override {
        _hoverAnim->stop();
        _hoverAnim->setStartValue(_hover);
        _hoverAnim->setEndValue(1.0);
        _hoverAnim->start();
    }
    void leaveEvent(QEvent *) override {
        _hoverAnim->stop();
        _hoverAnim->setStartValue(_hover);
        _hoverAnim->setEndValue(0.0);
        _hoverAnim->start();
    }
    void mousePressEvent(QMouseEvent *e) override {
        if (e->button() == Qt::LeftButton) emit clicked(data);
    }

    void paintEvent(QPaintEvent *) override {
        QPainter p(this);
        p.setRenderHints(QPainter::Antialiasing | QPainter::SmoothPixmapTransform);

        // Hover lift: scale from center
        if (_hover > 0.0) {
            qreal s  = 1.0 + _hover * 0.034;
            qreal tx = width()  * (1.0 - s) / 2.0;
            qreal ty = height() * (1.0 - s) / 2.0;
            p.translate(tx, ty);
            p.scale(s, s);
        }

        QRect r = rect();
        QPainterPath clip;
        clip.addRoundedRect(r, 10, 10);
        p.setClipPath(clip);

        if (!data.thumbnailPixmap.isNull()) {
            QPixmap sc = data.thumbnailPixmap.scaled(
                r.size(), Qt::KeepAspectRatioByExpanding, Qt::SmoothTransformation);
            int xo = (sc.width()  - r.width())  / 2;
            int yo = (sc.height() - r.height()) / 2;
            p.drawPixmap(0, 0, r.width(), r.height(), sc, xo, yo, r.width(), r.height());
        } else {
            p.fillRect(r, QColor("#13151c"));
        }

        // Hover sheen
        if (_hover > 0.0) {
            QLinearGradient sheen(0, 0, 0, r.height() * 0.5);
            sheen.setColorAt(0, QColor(255,255,255, int(26 * _hover)));
            sheen.setColorAt(1, Qt::transparent);
            p.fillRect(r, sheen);
        }

        p.setClipping(false);

        // Hover border glow
        if (_hover > 0.0) {
            p.setPen(QPen(QColor(255,255,255, int(50 * _hover)), 1.5));
            p.setBrush(Qt::NoBrush);
            p.drawRoundedRect(QRectF(r).adjusted(0.75,0.75,-0.75,-0.75), 10, 10);
        }

        // Bottom scrim (only when we have a thumb or hovering)
        if (!data.thumbnailPixmap.isNull()) {
            p.setClipPath(clip);
            QLinearGradient scrim(0, r.height()*0.42, 0, r.height());
            scrim.setColorAt(0, Qt::transparent);
            scrim.setColorAt(1, QColor(0,0,0,190));
            p.fillRect(r, scrim);
            p.setClipping(false);
        }

        // Badge font
        QFont f = p.font();
        f.setPointSizeF(8.4);
        p.setFont(f);
        QFontMetrics fm(f);

        // Caption badge (bottom-left)
        if (!data.caption.isEmpty()) {
            QString txt = fm.elidedText(data.caption, Qt::ElideRight, r.width() - 54);
            int tw = fm.horizontalAdvance(txt) + 12;
            QRect tr(7, r.bottom() - 25, tw, 17);
            p.setPen(Qt::NoPen);
            p.setBrush(QColor(8,10,16,215));
            p.drawRoundedRect(tr, 4, 4);
            p.setPen(QColor(208,212,222));
            p.drawText(tr, Qt::AlignCenter, txt);
        }

        // Score badge (bottom-right) — only shown when score > 0
        if (data.score > 0.0) {
            QString sc = QString::number(data.score, 'f', 2);
            int sw = fm.horizontalAdvance(sc) + 12;
            QRect sr(r.right() - 7 - sw, r.bottom() - 25, sw, 17);
            p.setPen(Qt::NoPen);
            p.setBrush(QColor(8,10,16,215));
            p.drawRoundedRect(sr, 4, 4);
            p.setPen(Qt::white);
            p.drawText(sr, Qt::AlignCenter, sc);
        }
    }

private:
    ShimmerWidget          *_shimmer   = nullptr;
    QPropertyAnimation     *_hoverAnim = nullptr;
    QGraphicsOpacityEffect *_fadeEff   = nullptr;
    qreal                   _hover     = 0.0;
};

// ═══════════════════════════════════════════════════════════════════
//  FLOW GRID  — responsive wrapping layout (no QListView)
// ═══════════════════════════════════════════════════════════════════

class FlowGrid : public QWidget {
    Q_OBJECT
public:
    int cardW = ImageCard::W;
    int cardH = ImageCard::H;
    const int gapH = 9;
    const int gapV = 9;
    QVector<ImageCard*> cards;

    explicit FlowGrid(QWidget *parent = nullptr) : QWidget(parent) {
        setSizePolicy(QSizePolicy::Expanding, QSizePolicy::Minimum);
    }

    void addCard(ImageCard *c) {
        c->setParent(this);
        cards.append(c);
        c->show();
        relayout();
    }

    void clearCards() {
        for (auto *c : cards) { c->hide(); c->deleteLater(); }
        cards.clear();
        setFixedHeight(8);
    }

    void resizeCards(int w, int h) {
        cardW = w; cardH = h;
        for (auto *c : cards) c->setFixedSize(w, h);
        relayout();
    }

    void relayout() {
        if (cards.isEmpty()) { setFixedHeight(8); return; }
        int W      = width();
        int cols   = qMax(1, (W + gapH) / (cardW + gapH));
        int gridW  = cols * (cardW + gapH) - gapH;
        int xStart = qMax(0, (W - gridW) / 2);

        for (int i = 0; i < cards.size(); ++i) {
            int col = i % cols;
            int row = i / cols;
            cards[i]->setFixedSize(cardW, cardH);
            cards[i]->move(xStart + col*(cardW+gapH), 8 + row*(cardH+gapV));
        }
        int rows   = (cards.size() + cols - 1) / cols;
        setFixedHeight(8 + rows*(cardH+gapV) - gapV + 8);
    }

protected:
    void resizeEvent(QResizeEvent *) override { relayout(); }
};

// ═══════════════════════════════════════════════════════════════════
//  LIGHTBOX OVERLAY
// ═══════════════════════════════════════════════════════════════════

class LightboxOverlay : public QWidget {
    Q_OBJECT
public:
    explicit LightboxOverlay(QWidget *parent) : QWidget(parent) {
        setAttribute(Qt::WA_NoSystemBackground);
        setVisible(false);
        _fadeAnim = new QPropertyAnimation(this, "windowOpacity", this);
        _fadeAnim->setDuration(180);
        _fadeAnim->setEasingCurve(QEasingCurve::OutCubic);
    }

    void showLoading(const QString &caption) {
        _pixmap  = QPixmap();
        _caption = caption;
        _loading = true;
        setVisible(true); raise(); update();
    }

    void showImage(const QPixmap &pix, const QString &caption) {
        _pixmap  = pix;
        _caption = caption;
        _loading = false;
        setVisible(true); raise();
        _fadeAnim->stop();
        _fadeAnim->setStartValue(0.0);
        _fadeAnim->setEndValue(1.0);
        _fadeAnim->start();
        update();
    }

    void setFullPixmap(const QPixmap &pix) {
        _pixmap = pix; _loading = false; update();
    }

protected:
    void paintEvent(QPaintEvent *) override {
        QPainter p(this);
        p.fillRect(rect(), QColor(4,5,8,240));
        if (_loading) {
            p.setPen(QColor("#525866"));
            p.drawText(rect(), Qt::AlignCenter, "Loading…");
            return;
        }
        if (_pixmap.isNull()) return;
        QRect area = rect().adjusted(80,80,-80,-80);
        QPixmap sc = _pixmap.scaled(area.size(), Qt::KeepAspectRatio, Qt::SmoothTransformation);
        int x = rect().center().x() - sc.width()/2;
        int y = rect().center().y() - sc.height()/2 - 20;
        p.drawPixmap(x, y, sc);

        QFont f = p.font(); f.setPointSize(11); p.setFont(f);
        p.setPen(QColor("#cdd0d8"));
        p.drawText(QRect(0, rect().bottom()-58, rect().width(), 36), Qt::AlignCenter, _caption);
        f.setPointSize(9); p.setFont(f);
        p.setPen(QColor("#404450"));
        p.drawText(QRect(0, rect().bottom()-28, rect().width(), 20), Qt::AlignCenter, "Click or Esc to close");
    }

    void mousePressEvent(QMouseEvent *) override {
        _fadeAnim->stop();
        _fadeAnim->setStartValue(1.0);
        _fadeAnim->setEndValue(0.0);
        connect(_fadeAnim, &QPropertyAnimation::finished, this, [this](){
            setVisible(false);
            disconnect(_fadeAnim, &QPropertyAnimation::finished, nullptr, nullptr);
        });
        _fadeAnim->start();
    }

private:
    QPixmap             _pixmap;
    QString             _caption;
    bool                _loading = false;
    QPropertyAnimation *_fadeAnim;
};

// ═══════════════════════════════════════════════════════════════════
//  TRAFFIC LIGHT BUTTON  — macOS-style coloured close/min/max dot
// ═══════════════════════════════════════════════════════════════════

class TrafficDot : public QWidget {
    Q_OBJECT
public:
    enum Action { Close, Minimize, Maximize };
    TrafficDot(Action act, const QColor &col, QWidget *parent = nullptr)
        : QWidget(parent), _action(act), _color(col) {
        setFixedSize(13, 13);
        setCursor(Qt::PointingHandCursor);
    }
signals:
    void triggered(Action);
protected:
    void enterEvent(QEnterEvent *)  override { _hovered = true;  update(); }
    void leaveEvent(QEvent *)       override { _hovered = false; update(); }
    void mousePressEvent(QMouseEvent *e) override {
        if (e->button() == Qt::LeftButton) emit triggered(_action);
    }
    void paintEvent(QPaintEvent *) override {
        QPainter p(this);
        p.setRenderHint(QPainter::Antialiasing);
        p.setPen(Qt::NoPen);
        p.setBrush(_hovered ? _color.lighter(110) : _color);
        p.drawEllipse(rect());
        if (_hovered) {
            p.setPen(QPen(QColor(0,0,0,90), 1.2));
            int cx = rect().center().x(), cy = rect().center().y();
            int d  = 3;
            if (_action == Close) {
                p.drawLine(cx-d, cy-d, cx+d, cy+d);
                p.drawLine(cx+d, cy-d, cx-d, cy+d);
            } else if (_action == Minimize) {
                p.drawLine(cx-d, cy, cx+d, cy);
            } else {
                p.drawLine(cx-d, cy, cx, cy-d);
                p.drawLine(cx,   cy-d, cx+d, cy);
                p.drawLine(cx+d, cy,   cx,   cy+d);
                p.drawLine(cx,   cy+d, cx-d, cy);
            }
        }
    }
private:
    Action _action;
    QColor _color;
    bool   _hovered = false;
};

// ═══════════════════════════════════════════════════════════════════
//  DRAG-HANDLE AREA  — lets user drag the frameless window
// ═══════════════════════════════════════════════════════════════════

class DragHandle : public QWidget {
    Q_OBJECT
public:
    explicit DragHandle(QWidget *mainWindow, QWidget *parent = nullptr)
        : QWidget(parent), _win(mainWindow) {}
protected:
    void mousePressEvent(QMouseEvent *e) override {
        if (e->button() == Qt::LeftButton)
            _dragStart = e->globalPosition().toPoint() - _win->frameGeometry().topLeft();
    }
    void mouseMoveEvent(QMouseEvent *e) override {
        if (e->buttons() & Qt::LeftButton)
            _win->move(e->globalPosition().toPoint() - _dragStart);
    }
private:
    QWidget *_win;
    QPoint   _dragStart;
};

// ═══════════════════════════════════════════════════════════════════
//  SIDEBAR BUTTON
// ═══════════════════════════════════════════════════════════════════

class SidebarBtn : public QPushButton {
public:
    SidebarBtn(const QString &label, QWidget *parent = nullptr)
        : QPushButton(parent), _label(label) {
        setCheckable(true);
        setAutoExclusive(true);
        setSizePolicy(QSizePolicy::Expanding, QSizePolicy::Fixed);
        setFixedHeight(33);

        auto *row = new QHBoxLayout(this);
        row->setContentsMargins(14, 0, 14, 0);

        _titleLbl = new QLabel(label, this);
        _titleLbl->setStyleSheet("background:transparent;font-size:13px;");
        _countLbl = new QLabel("", this);
        _countLbl->setStyleSheet("background:transparent;font-size:11px;color:#525866;");

        row->addWidget(_titleLbl);
        row->addStretch();
        row->addWidget(_countLbl);
    }

    void setCount(int n) {
        if (n < 0) { _countLbl->setText(""); return; }
        _countLbl->setText(QLocale().toString(n));
    }
    QString label() const { return _label; }

private:
    QLabel  *_titleLbl;
    QLabel  *_countLbl;
    QString  _label;
};

// ═══════════════════════════════════════════════════════════════════
//  MAIN WINDOW  — frameless, macOS-style
// ═══════════════════════════════════════════════════════════════════

class FotoroWindow : public QMainWindow {
    Q_OBJECT
public:
    FotoroWindow() {
        setWindowTitle("fotoro");
        setWindowFlags(Qt::FramelessWindowHint | Qt::Window);
        setAttribute(Qt::WA_TranslucentBackground, false);

        // Start maximised on primary screen
        QScreen *scr = QGuiApplication::primaryScreen();
        if (scr) setGeometry(scr->availableGeometry());
        setMinimumSize(800, 600);

        _net = new QNetworkAccessManager(this);

        auto *central = new QWidget(this);
        setCentralWidget(central);
        central->setObjectName("Central");

        // ── TITLE BAR ──────────────────────────────────────────────
        auto *titleBar = new DragHandle(this, central);
        titleBar->setObjectName("TitleBar");
        titleBar->setFixedHeight(46);

        auto *tbLayout = new QHBoxLayout(titleBar);
        tbLayout->setContentsMargins(16, 0, 20, 0);

        // Traffic lights (left side, Mac-style)
        auto *dots = new QHBoxLayout();
        dots->setSpacing(8);
        auto *btnClose = new TrafficDot(TrafficDot::Close,    QColor("#ff5f57"), titleBar);
        auto *btnMin   = new TrafficDot(TrafficDot::Minimize, QColor("#ffbd2e"), titleBar);
        auto *btnMax   = new TrafficDot(TrafficDot::Maximize, QColor("#28c841"), titleBar);
        dots->addWidget(btnClose);
        dots->addWidget(btnMin);
        dots->addWidget(btnMax);
        tbLayout->addLayout(dots);
        tbLayout->addStretch();

        // Centre badge (live-updated from /api/stats)
        _serverBadge = new QLabel("● fotoro.local", titleBar);
        _serverBadge->setObjectName("ServerBadge");
        tbLayout->addWidget(_serverBadge);
        tbLayout->addStretch();
        tbLayout->addSpacing(62); // visual balance

        connect(btnClose, &TrafficDot::triggered, [this](TrafficDot::Action){ close(); });
        connect(btnMin,   &TrafficDot::triggered, [this](TrafficDot::Action){ showMinimized(); });
        connect(btnMax,   &TrafficDot::triggered, [this](TrafficDot::Action){
            isMaximized() ? showNormal() : showMaximized();
        });

        // ── SIDEBAR ────────────────────────────────────────────────
        auto *sidebar = new QWidget(central);
        sidebar->setObjectName("Sidebar");
        sidebar->setFixedWidth(228);
        _sideLayout = new QVBoxLayout(sidebar);
        _sideLayout->setContentsMargins(8, 16, 8, 16);
        _sideLayout->setSpacing(2);

        // Static categories (counts fetched from /api/stats)
        struct CatDef { QString label; QString key; };
        const QVector<CatDef> cats = {
            {"Library",   ""},
            {"Favorites", "favorites"},
            {"People",    "people"},
            {"Places",    "places"},
            {"Events",    "events"},
        };
        bool firstCat = true;
        for (const auto &c : cats) {
            auto *btn = new SidebarBtn(c.label, sidebar);
            if (firstCat) { btn->setChecked(true); firstCat = false; }
            _catButtons.append(btn);
            _sideLayout->addWidget(btn);
            connect(btn, &QPushButton::clicked, [this, c]() {
                _activeCategory = c.key;
                _activeQuery    = "";
                _searchBar->clear();
                reloadGallery();
            });
        }

        // DEVICES section — fetched live from /api/devices (if your backend
        // exposes it), otherwise hidden. We add a placeholder that we populate
        // after startup via fetchStats().
        _devHeader = new QLabel("DEVICES", sidebar);
        _devHeader->setStyleSheet("color:#525866;font-size:9px;font-weight:bold;"
                                  "letter-spacing:0.08em;padding:16px 14px 5px 14px;"
                                  "background:transparent;");
        _devHeader->setVisible(false);     // hidden until we have real data
        _sideLayout->addWidget(_devHeader);
        _devContainer = new QWidget(sidebar);
        auto *devVL = new QVBoxLayout(_devContainer);
        devVL->setContentsMargins(0,0,0,0);
        devVL->setSpacing(2);
        _devLayout = devVL;
        _devContainer->setVisible(false);
        _sideLayout->addWidget(_devContainer);

        _sideLayout->addStretch();

        // ── SEARCH BAR ─────────────────────────────────────────────
        _searchBar = new QLineEdit(central);
        _searchBar->setObjectName("SearchBar");
        _searchBar->setPlaceholderText("Search your library…");
        _searchBar->setFixedHeight(40);
        // No ⌘K badge — clean

        // ── GALLERY ────────────────────────────────────────────────
        _scrollArea = new QScrollArea(central);
        _scrollArea->setObjectName("Gallery");
        _scrollArea->setWidgetResizable(true);
        _scrollArea->setHorizontalScrollBarPolicy(Qt::ScrollBarAlwaysOff);
        _scrollArea->setVerticalScrollBarPolicy(Qt::ScrollBarAsNeeded);

        _flowGrid = new FlowGrid();
        _scrollArea->setWidget(_flowGrid);

        // ── BOTTOM BAR ─────────────────────────────────────────────
        auto *bottomBar = new QWidget(central);
        bottomBar->setFixedHeight(36);
        auto *bbL = new QHBoxLayout(bottomBar);
        bbL->setContentsMargins(14, 0, 16, 0);

        _statusLabel = new QLabel("", bottomBar);
        _statusLabel->setObjectName("StatusLabel");

        _zoomSlider = new QSlider(Qt::Horizontal, bottomBar);
        _zoomSlider->setRange(110, 330);
        _zoomSlider->setValue(200);
        _zoomSlider->setFixedWidth(110);

        bbL->addWidget(_statusLabel);
        bbL->addStretch();
        bbL->addWidget(_zoomSlider);

        // ── RIGHT PANEL ────────────────────────────────────────────
        auto *rightVL = new QVBoxLayout();
        rightVL->setContentsMargins(12, 10, 12, 6);
        rightVL->setSpacing(10);
        rightVL->addWidget(_searchBar);
        rightVL->addWidget(_scrollArea, 1);
        rightVL->addWidget(bottomBar);

        // ── CONTENT ROW ────────────────────────────────────────────
        auto *contentRow = new QHBoxLayout();
        contentRow->setContentsMargins(0,0,0,0);
        contentRow->setSpacing(0);
        contentRow->addWidget(sidebar);
        contentRow->addLayout(rightVL);

        // ── ROOT ───────────────────────────────────────────────────
        auto *rootVL = new QVBoxLayout(central);
        rootVL->setContentsMargins(0,0,0,0);
        rootVL->setSpacing(0);
        rootVL->addWidget(titleBar);
        rootVL->addLayout(contentRow);

        // ── LIGHTBOX ───────────────────────────────────────────────
        _lightbox = new LightboxOverlay(central);

        // ── CONNECTIONS ────────────────────────────────────────────
        connect(_searchBar, &QLineEdit::returnPressed, this, &FotoroWindow::executeSearch);
        connect(_zoomSlider, &QSlider::valueChanged,   this, &FotoroWindow::onZoom);
        connect(_scrollArea->verticalScrollBar(), &QScrollBar::valueChanged,
                this, &FotoroWindow::checkInfiniteScroll);

        _relayoutTimer = new QTimer(this);
        _relayoutTimer->setSingleShot(true);
        _relayoutTimer->setInterval(55);
        connect(_relayoutTimer, &QTimer::timeout, [this](){ _flowGrid->relayout(); });

        // Fetch live stats first, then load gallery
        fetchStats();
        reloadGallery();
    }

protected:
    void resizeEvent(QResizeEvent *e) override {
        QMainWindow::resizeEvent(e);
        if (_lightbox) _lightbox->setGeometry(centralWidget()->rect());
        _relayoutTimer->start();
    }

    void keyPressEvent(QKeyEvent *e) override {
        if (e->key() == Qt::Key_Escape) {
            if (_lightbox->isVisible()) { _lightbox->hide(); return; }
            // Esc clears search
            if (!_searchBar->text().isEmpty()) {
                _searchBar->clear();
                reloadGallery();
                return;
            }
        }
        if ((e->modifiers() & Qt::ControlModifier) && e->key() == Qt::Key_F) {
            _searchBar->setFocus();
            _searchBar->selectAll();
        }
        QMainWindow::keyPressEvent(e);
    }

private slots:
    // ── Fetch /api/stats for live counts ──────────────────────────
    void fetchStats() {
        auto *reply = _net->get(QNetworkRequest(QUrl(BASE_URL + "/api/stats")));
        connect(reply, &QNetworkReply::finished, this, [this, reply]() {
            if (reply->error() == QNetworkReply::NoError) {
                auto obj = QJsonDocument::fromJson(reply->readAll()).object();
                int total = obj.value("total").toInt();

                // Update server badge
                _serverBadge->setText(
                    QString("● fotoro.local · %1 items")
                        .arg(QLocale().toString(total)));

                // Update Library count (first cat button)
                if (!_catButtons.isEmpty())
                    _catButtons[0]->setCount(total);

                // If backend later adds per-category counts to /api/stats,
                // map them here. For now only total is reliable.
            }
            reply->deleteLater();
        });
    }

    void onZoom(int v) {
        // 4:3-ish cards
        _flowGrid->resizeCards(int(v * 1.37), v);
    }

    void checkInfiniteScroll(int val) {
        if (_activeQuery.isEmpty() && !_fetching) {
            auto *sb = _scrollArea->verticalScrollBar();
            bool nearBottom = (val >= sb->maximum() - 250);
            bool fitsView   = (sb->maximum() == 0);
            if ((nearBottom || fitsView) && _totalFetched > 0 && _totalFetched % 50 == 0) {
                _page++;
                fetchPage(_page);
            }
        }
    }

    void reloadGallery() {
        _page = 1; _totalFetched = 0;
        _flowGrid->clearCards();
        fetchPage(_page);
    }

    void fetchPage(int page) {
        if (_fetching) return;
        _fetching = true;
        _statusLabel->setText("Loading…");

        QString url = QString("%1/api/images?page=%2&per_page=50&sort=date_desc")
                          .arg(BASE_URL).arg(page);
        if (!_activeCategory.isEmpty())
            url += "&category=" + QUrl::toPercentEncoding(_activeCategory);

        auto *reply = _net->get(QNetworkRequest(QUrl(url)));
        connect(reply, &QNetworkReply::finished, this, [this, reply]() {
            _fetching = false;
            if (reply->error() != QNetworkReply::NoError) {
                _statusLabel->setText("Cannot reach fotoro server");
                reply->deleteLater();
                return;
            }
            auto doc = QJsonDocument::fromJson(reply->readAll());
            // /api/images returns a plain JSON array
            QJsonArray arr = doc.isArray() ? doc.array()
                                           : doc.object().value("results").toArray();
            for (const auto &v : arr) {
                auto obj = v.toObject();
                ImageData d;
                d.hash          = obj.value("hash").toString();
                d.caption       = obj.value("caption").toString();
                d.category      = obj.value("category").toString();
                d.thumbnailPath = obj.value("thumbnail").toString();
                d.score         = 0.0;  // browse — no score badge
                addCard(d);
            }
            _totalFetched += arr.size();
            _statusLabel->setText(QLocale().toString(_totalFetched) + " items");
            reply->deleteLater();

            QTimer::singleShot(100, this, [this](){
                checkInfiniteScroll(_scrollArea->verticalScrollBar()->value());
            });
        });
    }

    void executeSearch() {
        _activeQuery = _searchBar->text().trimmed();
        if (_activeQuery.isEmpty()) { reloadGallery(); return; }

        _page = 1; _totalFetched = 0;
        _flowGrid->clearCards();
        _statusLabel->setText("Searching…");

        // /api/search wraps results in {"query":…,"results":[…]}
        // score field comes as a string "0.943" — we handle both
        QString url = QString("%1/api/search?q=%2")
                          .arg(BASE_URL)
                          .arg(QString::fromUtf8(QUrl::toPercentEncoding(_activeQuery)));

        auto *reply = _net->get(QNetworkRequest(QUrl(url)));
        connect(reply, &QNetworkReply::finished, this, [this, reply]() {
            if (reply->error() != QNetworkReply::NoError) {
                _statusLabel->setText("Search failed — check server");
                reply->deleteLater();
                return;
            }
            auto doc = QJsonDocument::fromJson(reply->readAll());
            QJsonArray arr = doc.isArray() ? doc.array()
                                           : doc.object().value("results").toArray();

            int count = 0;
            for (const auto &v : arr) {
                auto obj = v.toObject();
                ImageData d;
                d.hash          = obj.value("hash").toString();
                d.caption       = obj.value("caption").toString();
                d.category      = obj.value("category").toString();
                d.thumbnailPath = obj.value("thumbnail").toString();

                // score is returned as a STRING "0.943" by the Go server
                // (fmt.Sprintf("%.3f", …)) — parse it correctly
                QJsonValue sv = obj.value("score");
                if (sv.isString())
                    d.score = sv.toString().toDouble();
                else
                    d.score = sv.toDouble();

                // Only show score badge for real semantic results
                // (fallback results have score == 0.0 or score string "0.000")
                addCard(d);
                ++count;
            }
            _totalFetched = count;
            if (count == 0)
                _statusLabel->setText("No results");
            else
                _statusLabel->setText(QString("%1 results").arg(count));
            reply->deleteLater();
        });
    }

    void addCard(const ImageData &d) {
        auto *card = new ImageCard(_flowGrid);
        card->setCardData(d);
        _flowGrid->addCard(card);
        connect(card, &ImageCard::clicked, this, &FotoroWindow::onCardClicked);

        // Staggered entrance fade
        int idx = _flowGrid->cards.size() - 1;
        card->fadeEffect()->setOpacity(0.0);
        auto *anim = new QPropertyAnimation(card->fadeEffect(), "opacity", card);
        anim->setStartValue(0.0);
        anim->setEndValue(1.0);
        anim->setDuration(200);
        anim->setEasingCurve(QEasingCurve::OutCubic);
        QTimer::singleShot(qMin(idx * 16, 380), anim,
            [anim](){ anim->start(QAbstractAnimation::DeleteWhenStopped); });

        if (!d.thumbnailPath.isEmpty())
            fetchThumbnail(card, d.thumbnailPath);
    }

    void fetchThumbnail(ImageCard *card, const QString &path) {
        QPointer<ImageCard> safe = card;
        // thumbnailPath is already a full relative URL like /api/thumbnail/HASH?size=small
        QString url = BASE_URL + path;
        auto *reply = _net->get(QNetworkRequest(QUrl(url)));
        connect(reply, &QNetworkReply::finished, this, [this, reply, safe]() {
            if (!safe) { reply->deleteLater(); return; }
            if (reply->error() == QNetworkReply::NoError) {
                QPixmap pix;
                if (pix.loadFromData(reply->readAll()))
                    safe->setThumbnail(pix);
            }
            reply->deleteLater();
        });
    }

    void onCardClicked(const ImageData &d) {
        _lightbox->showLoading(d.caption);
        _lightbox->setGeometry(centralWidget()->rect());
        _lightbox->raise();

        auto *reply = _net->get(QNetworkRequest(
            QUrl(BASE_URL + "/api/image/" + d.hash)));
        connect(reply, &QNetworkReply::finished, this, [this, reply, d]() {
            if (reply->error() == QNetworkReply::NoError) {
                QPixmap pix;
                if (pix.loadFromData(reply->readAll()))
                    _lightbox->showImage(pix, d.caption);
            }
            reply->deleteLater();
        });
    }

private:
    QNetworkAccessManager *_net           = nullptr;
    FlowGrid              *_flowGrid      = nullptr;
    QScrollArea           *_scrollArea    = nullptr;
    QLineEdit             *_searchBar     = nullptr;
    QSlider               *_zoomSlider    = nullptr;
    QLabel                *_statusLabel   = nullptr;
    QLabel                *_serverBadge   = nullptr;
    LightboxOverlay       *_lightbox      = nullptr;
    QTimer                *_relayoutTimer = nullptr;
    QVBoxLayout           *_sideLayout    = nullptr;
    QVBoxLayout           *_devLayout     = nullptr;
    QWidget               *_devContainer  = nullptr;
    QLabel                *_devHeader     = nullptr;
    QVector<SidebarBtn*>   _catButtons;

    QString _activeCategory;
    QString _activeQuery;
    int     _page         = 1;
    int     _totalFetched = 0;
    bool    _fetching     = false;
};

// ═══════════════════════════════════════════════════════════════════
//  STYLESHEET
// ═══════════════════════════════════════════════════════════════════

static const char *QSS = R"(
QMainWindow, QWidget {
    background-color: #090a0f;
    color: #c4c8d4;
    font-family: -apple-system, "SF Pro Display", "Segoe UI", "Helvetica Neue", Arial, sans-serif;
}

QWidget#Central {
    background-color: #090a0f;
}

QWidget#TitleBar {
    background-color: #090a0f;
    border-bottom: 1px solid #12141c;
}

QLabel#ServerBadge {
    background-color: #0c0d14;
    border: 1px solid #181a24;
    border-radius: 11px;
    color: #5c6070;
    padding: 3px 14px;
    font-size: 11px;
    font-family: "SF Mono", "JetBrains Mono", monospace;
}

QWidget#Sidebar {
    background-color: #0a0b11;
    border-right: 1px solid #12141c;
}

SidebarBtn {
    color: #7a8090;
    background-color: transparent;
    border: none;
    border-radius: 6px;
    text-align: left;
}
SidebarBtn:hover {
    background-color: #11141e;
    color: #dde0ea;
}
SidebarBtn:checked {
    background-color: #14172a;
    color: #e8eaff;
    font-weight: 600;
}

QLineEdit#SearchBar {
    background-color: #0d0f18;
    border: 1px solid #1a1e2c;
    border-radius: 8px;
    color: #e0e3ef;
    padding: 0 14px;
    font-size: 13px;
    selection-background-color: #2a3260;
}
QLineEdit#SearchBar:focus {
    border-color: #252a40;
    background-color: #0f1220;
}

QScrollArea#Gallery {
    background-color: #090a0f;
    border: none;
}
QScrollArea#Gallery > QWidget > QWidget {
    background-color: #090a0f;
}

QScrollBar:vertical {
    background: #090a0f;
    width: 6px;
    margin: 0;
}
QScrollBar::handle:vertical {
    background: #1c2030;
    min-height: 26px;
    border-radius: 3px;
}
QScrollBar::handle:vertical:hover { background: #2c3250; }
QScrollBar::add-line:vertical, QScrollBar::sub-line:vertical {
    border: none; background: none; height: 0;
}

QSlider::groove:horizontal {
    height: 3px;
    background: #1a1e2c;
    border-radius: 1.5px;
}
QSlider::handle:horizontal {
    background: #7a8090;
    width: 11px; height: 11px;
    margin: -4px 0;
    border-radius: 5.5px;
}
QSlider::handle:horizontal:hover { background: #e0e3ef; }
QSlider::sub-page:horizontal {
    background: #2a3880;
    border-radius: 1.5px;
}

QLabel#StatusLabel {
    color: #44485a;
    font-size: 11px;
}
)";

// ═══════════════════════════════════════════════════════════════════
//  ENTRY POINT
// ═══════════════════════════════════════════════════════════════════

int main(int argc, char *argv[]) {
    QApplication app(argc, argv);
    app.setStyleSheet(QSS);

    FotoroWindow w;
    w.show();
    return app.exec();
}

#include "main.moc"
