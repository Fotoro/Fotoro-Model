/*  fotoro — desktop photo search (Qt6)
 *  Monochrome design system: Geist, pure black canvas, zinc-gray structure
 */

#include <QApplication>
#include <QMainWindow>
#include <QWidget>
#include <QHBoxLayout>
#include <QVBoxLayout>
#include <QGridLayout>
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
#include <QWheelEvent>
#include <QSet>
#include <QSettings>
#include <QUrl>
#include <QStackedWidget>
#include <QComboBox>
#include <QCheckBox>
#include <QTimeEdit>
#include <QSpinBox>
#include <QTextEdit>
#include <QProgressBar>
#include <QGraphicsDropShadowEffect>
#include <QFontDatabase>
#include <QFile>
#include <QMessageBox>
#include <QInputDialog>
#include <QDialog>
#include <QDialogButtonBox>
#include <QFormLayout>
#include <QStackedLayout>
#include <QSplitter>
#include <QToolButton>
#include <QMenu>
#include <QAction>
#include <QDir>
#include <QLinearGradient>
#include <QLocale>
#include <QTime>
#include <QTransform>
#include <QDesktopServices>
#include <QCloseEvent>
#include <cmath>
#include <algorithm>

static const QString BASE_URL = "http://127.0.0.1:8765";

// ═══════════════════════════════════════════════════════════════════
//  FONT LOADING
// ═══════════════════════════════════════════════════════════════════

static void loadFonts() {
    const QString home = QDir::homePath();
    QStringList sansPaths = {
        ":/fonts/Geist-Regular.ttf",
        ":/fonts/Geist-Medium.ttf",
        ":/fonts/Geist-SemiBold.ttf",
        home + "/.local/share/fonts/Geist-Regular.ttf",
        home + "/.fonts/Geist-Regular.ttf",
        "/usr/share/fonts/geist/Geist-Regular.ttf",
        "/usr/local/share/fonts/Geist-Regular.ttf",
    };
    QStringList monoPaths = {
        ":/fonts/GeistMono-Regular.ttf",
        home + "/.local/share/fonts/GeistMono-Regular.ttf",
        home + "/.fonts/GeistMono-Regular.ttf",
        "/usr/share/fonts/geist/GeistMono-Regular.ttf",
        "/usr/local/share/fonts/GeistMono-Regular.ttf",
    };
    for (const auto& path : sansPaths) {
        if (QFile::exists(path)) { QFontDatabase::addApplicationFont(path); break; }
    }
    for (const auto& path : monoPaths) {
        if (QFile::exists(path)) { QFontDatabase::addApplicationFont(path); break; }
    }
}

static QFont uiFont(qreal pts, int weight = QFont::Normal) {
    QFont f;
    f.setFamilies({"Geist", "Inter", "Segoe UI", "Helvetica Neue", "Arial", "sans-serif"});
    f.setPointSizeF(pts);
    f.setWeight(static_cast<QFont::Weight>(weight));
    f.setLetterSpacing(QFont::PercentageSpacing, 98);
    f.setStyleStrategy(QFont::PreferAntialias);
    f.setHintingPreference(QFont::PreferFullHinting);
    return f;
}

static QFont monoFont(qreal pts) {
    QFont f;
    f.setFamilies({"Geist Mono", "JetBrains Mono", "SF Mono", "Ubuntu Mono", "monospace"});
    f.setPointSizeF(pts);
    f.setLetterSpacing(QFont::PercentageSpacing, 97);
    f.setStyleStrategy(QFont::PreferAntialias);
    return f;
}

static QFont eyebrowFont() {
    QFont f = uiFont(9, QFont::Medium);
    f.setLetterSpacing(QFont::AbsoluteSpacing, 2.5);
    return f;
}

// Fotoro design tokens — monochrome HSL grayscale, no hue
namespace Colors {
    static const QColor background      = QColor("#000000");
    static const QColor foreground      = QColor("#F5F5F5");
    static const QColor card            = QColor("#0D0D0D");
    static const QColor muted           = QColor("#1A1A1A");
    static const QColor secondary       = QColor("#1F1F1F");
    static const QColor accent          = QColor("#242424");
    static const QColor border          = QColor("#242424");
    static const QColor mutedForeground = QColor("#9E9E9E");
    static const QColor primary         = QColor("#F5F5F5");
    static const QColor primaryFg       = QColor("#0F0F0F");
    static const QColor ring            = QColor("#F5F5F5");
    static const QColor destructive     = QColor("#EBEBEB");
    static const QColor destructiveFg   = QColor("#0F0F0F");

    // semantic aliases used throughout widgets
    static const QColor bgBase        = background;
    static const QColor bgSurface     = card;
    static const QColor bgElevated    = secondary;
    static const QColor bgCard        = card;
    static const QColor bgHover       = accent;
    static const QColor borderSubtle  = border;
    static const QColor borderDefault = border;
    static const QColor textPrimary   = foreground;
    static const QColor textSecondary = mutedForeground;
    static const QColor textTertiary  = mutedForeground;
    static const QColor textMuted     = mutedForeground;
}

static QString pillBadgeStyle(const QColor &fg = Colors::mutedForeground) {
    return QString(
        "QLabel { background-color: rgba(255,255,255,0.06);"
        " border: 1px solid %1; border-radius: 999px; color: %2;"
        " padding: 5px 14px; min-height: 22px; }")
        .arg(Colors::border.name(), fg.name());
}

static void saveAuthToken(const QString &token) {
    QSettings s("fotoro", "fotoro");
    s.setValue("auth/access_token", token);
}

static QString loadAuthToken() {
    return QSettings("fotoro", "fotoro").value("auth/access_token").toString();
}

static QNetworkRequest apiRequest(const QUrl &url) {
    QNetworkRequest req(url);
    const QString tok = loadAuthToken();
    if (!tok.isEmpty())
        req.setRawHeader("Authorization", ("Bearer " + tok).toUtf8());
    return req;
}

static void paintSoftRing(QPainter& p, const QRect& r, int radius = 12) {
    QPainterPath path;
    path.addRoundedRect(r, radius, radius);
    p.fillPath(path, Colors::card);
    p.setPen(QPen(Colors::border, 1));
    p.drawPath(path);
    QPainterPath highlight;
    highlight.addRoundedRect(r.adjusted(1, 1, -1, r.height() / 2), radius - 1, radius - 1);
    p.setPen(Qt::NoPen);
    p.fillPath(highlight, QColor(255, 255, 255, 10));
}

// ═══════════════════════════════════════════════════════════════════
//  DATA MODEL
// ═══════════════════════════════════════════════════════════════════

struct ImageData {
    QString hash;
    QString caption;
    QString category;
    QString thumbnailPath;
    QPixmap thumbnailPixmap;
    double  score       = 0.0;
    bool    isFavorite  = false;
    float   aspectRatio = 4.0f / 3.0f;
};

// ═══════════════════════════════════════════════════════════════════
//  CIRCULAR BUTTON
// ═══════════════════════════════════════════════════════════════════

class FotoroButton : public QPushButton {
    Q_OBJECT
public:
    enum Style { Ghost, Primary, Outline, Secondary, Destructive };
    explicit FotoroButton(const QString& text, Style style = Ghost, QWidget* p = nullptr)
        : QPushButton(text, p), _style(style) {
        setAttribute(Qt::WA_Hover);
        setCursor(Qt::PointingHandCursor);
        setFixedHeight(40);
        setMinimumWidth(72);
        _hoverAnim = new QVariantAnimation(this);
        _hoverAnim->setDuration(150);
        _hoverAnim->setEasingCurve(QEasingCurve::OutCubic);
        connect(_hoverAnim, &QVariantAnimation::valueChanged,
                [this](const QVariant& v) { _hover = v.toReal(); update(); });
    }
    void setCompact(bool v) { setFixedHeight(v ? 32 : 40); update(); }
protected:
    void enterEvent(QEnterEvent*) override {
        _hoverAnim->stop();
        _hoverAnim->setStartValue(_hover);
        _hoverAnim->setEndValue(1.0);
        _hoverAnim->start();
    }
    void leaveEvent(QEvent*) override {
        _hoverAnim->stop();
        _hoverAnim->setStartValue(_hover);
        _hoverAnim->setEndValue(0.0);
        _hoverAnim->start();
    }
    void paintEvent(QPaintEvent*) override {
        QPainter p(this);
        p.setRenderHint(QPainter::Antialiasing);
        QRect r = rect().adjusted(1, 1, -2, -2);
        const int radius = height() <= 32 ? 6 : 8;
        QPainterPath path;
        path.addRoundedRect(r, radius, radius);
        QColor bg, fg, border;
        switch (_style) {
            case Primary:
                bg = QColor::fromRgbF(0.96, 0.96, 0.96, 0.92 + 0.08 * _hover);
                fg = Colors::primaryFg;
                border = QColor(255, 255, 255, int(40 + 30 * _hover));
                break;
            case Outline:
                bg = QColor::fromRgbF(0.14, 0.14, 0.14, 0.2 + 0.5 * _hover);
                fg = Colors::foreground;
                border = Colors::border;
                break;
            case Secondary:
                bg = QColor::fromRgbF(0.12, 0.12, 0.12, 0.8 + 0.2 * _hover);
                fg = Colors::foreground;
                border = Colors::border;
                break;
            case Destructive:
                bg = QColor::fromRgbF(0.92, 0.92, 0.92, 0.85 + 0.15 * _hover);
                fg = Colors::destructiveFg;
                border = QColor(255, 255, 255, 30);
                break;
            case Ghost:
            default:
                bg = QColor::fromRgbF(0.14, 0.14, 0.14, 0.0 + 0.85 * _hover);
                fg = Colors::mutedForeground;
                border = Qt::transparent;
                break;
        }
        p.fillPath(path, bg);
        if (border.alpha() > 0) {
            p.setPen(QPen(border, 1.0));
            p.drawPath(path);
        }
        if (_style == Primary && _hover > 0) {
            QPainterPath inset;
            inset.addRoundedRect(r.adjusted(1, 1, -1, -r.height() / 2), radius - 1, radius - 1);
            p.fillPath(inset, QColor(255, 255, 255, int(25 * _hover)));
        }
        p.setPen(fg);
        p.setFont(uiFont(height() <= 32 ? 10 : 11,
            _style == Primary ? QFont::DemiBold : QFont::Medium));
        p.drawText(r, Qt::AlignCenter, text());
    }
private:
    Style _style = Ghost;
    qreal _hover = 0.0;
    QVariantAnimation* _hoverAnim;
};

using CircularButton = FotoroButton;

// ═══════════════════════════════════════════════════════════════════
//  CIRCULAR ICON BUTTON
// ═══════════════════════════════════════════════════════════════════

class IconButton : public QPushButton {
    Q_OBJECT
public:
    enum Icon { Settings, Search };
    explicit IconButton(Icon icon, QWidget* p = nullptr)
        : QPushButton(p), _icon(icon) {
        setFixedSize(36, 36);
        setAttribute(Qt::WA_Hover);
        setCursor(Qt::PointingHandCursor);
        setCheckable(true);
        _hoverAnim = new QVariantAnimation(this);
        _hoverAnim->setDuration(180);
        _hoverAnim->setEasingCurve(QEasingCurve::OutCubic);
        connect(_hoverAnim, &QVariantAnimation::valueChanged,
                [this](const QVariant& v) { _hover = v.toReal(); update(); });
    }
protected:
    void enterEvent(QEnterEvent*) override {
        _hoverAnim->stop();
        _hoverAnim->setStartValue(_hover);
        _hoverAnim->setEndValue(1.0);
        _hoverAnim->start();
    }
    void leaveEvent(QEvent*) override {
        _hoverAnim->stop();
        _hoverAnim->setStartValue(_hover);
        _hoverAnim->setEndValue(0.0);
        _hoverAnim->start();
    }
    void paintEvent(QPaintEvent*) override {
        QPainter p(this);
        p.setRenderHint(QPainter::Antialiasing);
        QRect r = rect().adjusted(2, 2, -2, -2);
        QPainterPath path;
        path.addRoundedRect(r, 6, 6);
        QColor bg = isChecked() ? Colors::accent : Colors::secondary;
        if (_hover > 0 && !isChecked()) {
            bg = QColor::fromRgbF(
                bg.redF()   + (Colors::accent.redF()   - bg.redF())   * _hover,
                bg.greenF() + (Colors::accent.greenF() - bg.greenF()) * _hover,
                bg.blueF()  + (Colors::accent.blueF()  - bg.blueF())  * _hover, 1.0);
        }
        p.fillPath(path, bg);
        p.setPen(QPen(Colors::border, 1));
        p.drawPath(path);
        p.setPen(QPen(isChecked() ? Colors::foreground : Colors::mutedForeground, 1.4,
                      Qt::SolidLine, Qt::RoundCap, Qt::RoundJoin));
        const qreal cx = r.center().x(), cy = r.center().y();
        if (_icon == Settings) {
            p.setBrush(Qt::NoBrush);
            p.drawEllipse(QPointF(cx, cy), 5.5, 5.5);
            for (int i = 0; i < 8; ++i) {
                qreal a = i * M_PI / 4.0;
                p.drawLine(QPointF(cx + 6.5 * cos(a), cy + 6.5 * sin(a)),
                           QPointF(cx + 8.5 * cos(a), cy + 8.5 * sin(a)));
            }
        } else {
            p.setBrush(Qt::NoBrush);
            p.drawEllipse(QPointF(cx, cy), 5.5, 5.5);
            p.drawLine(QPointF(cx + 4, cy + 4), QPointF(cx + 7.5, cy + 7.5));
        }
    }
private:
    Icon _icon;
    qreal _hover = 0.0;
    QVariantAnimation* _hoverAnim;
};

using CircularIconBtn = IconButton;
// ═══════════════════════════════════════════════════════════════════
//  SHIMMER LOADING WIDGET
// ═══════════════════════════════════════════════════════════════════

class ShimmerWidget : public QWidget {
    Q_OBJECT
public:
    explicit ShimmerWidget(QWidget *p = nullptr) : QWidget(p) {
        setAttribute(Qt::WA_OpaquePaintEvent, false);
        _anim = new QVariantAnimation(this);
        _anim->setStartValue(0.0); _anim->setEndValue(1.0);
        _anim->setDuration(1600); _anim->setLoopCount(-1);
        _anim->setEasingCurve(QEasingCurve::Linear);
        connect(_anim, &QVariantAnimation::valueChanged,
                [this](const QVariant &v){ _pos = v.toReal(); update(); });
        _anim->start();
    }
    void stop() { _anim->stop(); hide(); }
protected:
    void paintEvent(QPaintEvent *) override {
        QPainter p(this);
        p.setRenderHint(QPainter::Antialiasing);
        QPainterPath clip; clip.addRoundedRect(rect(), 16, 16);
        p.setClipPath(clip);
        p.fillRect(rect(), Colors::bgCard);
        qreal x = _pos * (width() + 400) - 200;
        QLinearGradient g(x-150, 0, x+150, height());
        g.setColorAt(0.0, QColor(255,255,255,0));
        g.setColorAt(0.5, QColor(255,255,255,15));
        g.setColorAt(1.0, QColor(255,255,255,0));
        p.fillRect(rect(), g);
    }
private:
    qreal _pos = 0.0;
    QVariantAnimation *_anim;
};

// ═══════════════════════════════════════════════════════════════════
//  HEART BUTTON
// ═══════════════════════════════════════════════════════════════════

class HeartBtn : public QWidget {
    Q_OBJECT
public:
    explicit HeartBtn(QWidget *p = nullptr) : QWidget(p) {
        setFixedSize(32, 32);
        setCursor(Qt::PointingHandCursor);
        setAttribute(Qt::WA_Hover);
        _anim = new QVariantAnimation(this);
        _anim->setDuration(200);
        _anim->setStartValue(1.0); _anim->setEndValue(1.3);
        _anim->setEasingCurve(QEasingCurve::OutBack);
        connect(_anim, &QVariantAnimation::valueChanged,
                [this](const QVariant &v){ _scale = v.toReal(); update(); });
    }
    bool isFav() const { return _fav; }
    void setFav(bool v) { _fav = v; update(); }
signals:
    void toggled(bool isFav);
protected:
    void mousePressEvent(QMouseEvent *e) override {
        if (e->button() == Qt::LeftButton) {
            e->accept();
            _fav = !_fav;
            _anim->setDirection(_fav ? QAbstractAnimation::Forward : QAbstractAnimation::Backward);
            _anim->start();
            update();
            emit toggled(_fav);
        }
    }
    void paintEvent(QPaintEvent *) override {
        QPainter p(this);
        p.setRenderHint(QPainter::Antialiasing);
        p.translate(width()/2.0, height()/2.0);
        p.scale(_scale, _scale);
        QPainterPath heart;
        heart.moveTo(0, 3);
        heart.cubicTo(-1, 1, -8, -3, -8, -6);
        heart.arcTo(QRectF(-8,-10,8,8), 180, -180);
        heart.arcTo(QRectF(0,-10,8,8), 180, 180);
        heart.cubicTo(8, -3, 1, 1, 0, 3);
        p.translate(-0.5, 0.5);
        if (_fav) {
            p.setBrush(Colors::foreground);
            p.setPen(Qt::NoPen);
        } else {
            p.setBrush(QColor(0, 0, 0, 120));
            p.setPen(QPen(QColor(255, 255, 255, 140), 1.2));
        }
        p.drawPath(heart);
    }
private:
    bool _fav = false;
    qreal _scale = 1.0;
    QVariantAnimation *_anim;
};

// ═══════════════════════════════════════════════════════════════════
//  IMAGE CARD
// ═══════════════════════════════════════════════════════════════════

class ImageCard : public QFrame {
    Q_OBJECT
    Q_PROPERTY(qreal hover READ hover WRITE setHover)
public:
    static const int BASE_W = 240;
    ImageData data;
    
    explicit ImageCard(QWidget *parent = nullptr) : QFrame(parent) {
        setAttribute(Qt::WA_Hover);
        setCursor(Qt::PointingHandCursor);
        setFixedSize(BASE_W, 180);
        setFrameStyle(QFrame::NoFrame);
        _shimmer = new ShimmerWidget(this);
        _shimmer->setGeometry(0, 0, BASE_W, 180);
        _hoverAnim = new QPropertyAnimation(this, "hover", this);
        _hoverAnim->setDuration(200);
        _hoverAnim->setEasingCurve(QEasingCurve::OutCubic);
        _fadeEff = new QGraphicsOpacityEffect(this);
        _fadeEff->setOpacity(0.0);
        setGraphicsEffect(_fadeEff);
        _heart = new HeartBtn(this);
        _heart->move(width() - 38, 8);
        _heart->hide();
        connect(_heart, &HeartBtn::toggled, this, [this](bool fav){
            data.isFavorite = fav;
            emit favoriteToggled(data.hash, fav);
        });
    }
    void setCardData(const ImageData &d) {
        data = d;
        _heart->setFav(d.isFavorite);
        update();
    }
    void setThumbnail(const QPixmap &pix) {
        data.thumbnailPixmap = pix;
        if (pix.width() > 0 && pix.height() > 0)
            data.aspectRatio = float(pix.width()) / float(pix.height());
        if (_shimmer) _shimmer->stop();
        auto *a = new QPropertyAnimation(_fadeEff, "opacity", this);
        a->setStartValue(_fadeEff->opacity());
        a->setEndValue(1.0);
        a->setDuration(300);
        a->setEasingCurve(QEasingCurve::OutCubic);
        a->start(QAbstractAnimation::DeleteWhenStopped);
        update();
    }
    QGraphicsOpacityEffect *fadeEffect() { return _fadeEff; }
    qreal hover() const { return _hover; }
    void setHover(qreal v) { _hover = v; update(); }
    void resizeToWidth(int w) {
        _shimmer->setGeometry(0, 0, w, height());
        _heart->move(w - 38, 8);
    }
signals:
    void clicked(const ImageData &data);
    void favoriteToggled(const QString &hash, bool fav);
protected:
    void enterEvent(QEnterEvent *) override {
        _hoverAnim->stop();
        _hoverAnim->setStartValue(_hover);
        _hoverAnim->setEndValue(1.0);
        _hoverAnim->start();
        _heart->show();
        raise();
    }
    void leaveEvent(QEvent *) override {
        _hoverAnim->stop();
        _hoverAnim->setStartValue(_hover);
        _hoverAnim->setEndValue(0.0);
        _hoverAnim->start();
        if (!_heart->isFav()) _heart->hide();
    }
    void mousePressEvent(QMouseEvent *e) override {
        if (e->button() == Qt::LeftButton) {
            QPoint hPos = _heart->mapFromParent(e->pos());
            if (!_heart->rect().contains(hPos))
                emit clicked(data);
        }
    }
    void resizeEvent(QResizeEvent *) override {
        _heart->move(width() - 38, 8);
        if (_shimmer) _shimmer->setGeometry(0, 0, width(), height());
    }
    void paintEvent(QPaintEvent *) override {
        QPainter p(this);
        p.setRenderHints(QPainter::Antialiasing | QPainter::SmoothPixmapTransform);
        QTransform tf;
        if (_hover > 0.0) {
            qreal s  = 1.0 + _hover * 0.025;
            qreal ty = -_hover * 4.0;
            qreal tx = width()  * (1.0 - s) / 2.0;
            qreal tw = height() * (1.0 - s) / 2.0 + ty;
            p.setTransform(tf.translate(tx, tw).scale(s, s));
        }
        QRect r = rect();
        QPainterPath clip; clip.addRoundedRect(r, 16, 16);
        p.setClipPath(clip);
        if (_hover > 0.0) {
            p.setClipping(false);
            p.setTransform(QTransform());
            QPainterPath shadowPath;
            shadowPath.addRoundedRect(r.adjusted(-2,-2,2,2), 18, 18);
            QColor glow = Colors::foreground;
            glow.setAlpha(int(18 * _hover));
            p.fillPath(shadowPath, glow);
            p.setTransform(tf);
            if (_hover > 0.0) {
                qreal s  = 1.0 + _hover * 0.025;
                qreal ty = -_hover * 4.0;
                qreal tx = r.width()  * (1.0 - s) / 2.0;
                qreal tw = r.height() * (1.0 - s) / 2.0 + ty;
                p.setTransform(QTransform().translate(tx,tw).scale(s,s));
            }
            p.setClipPath(clip);
        }
        if (!data.thumbnailPixmap.isNull()) {
            QPixmap sc = data.thumbnailPixmap.scaled(
                r.size(), Qt::KeepAspectRatioByExpanding, Qt::SmoothTransformation);
            int xo = (sc.width()  - r.width())  / 2;
            int yo = (sc.height() - r.height()) / 2;
            p.drawPixmap(0, 0, r.width(), r.height(), sc, xo, yo, r.width(), r.height());
        } else {
            p.fillRect(r, Colors::bgCard);
        }
        if (_hover > 0.0) {
            QLinearGradient sheen(0, 0, 0, r.height() * 0.5);
            sheen.setColorAt(0, QColor(255,255,255, int(20 * _hover)));
            sheen.setColorAt(1, Qt::transparent);
            p.fillRect(r, sheen);
        }
        p.setClipping(false);
        p.setTransform(QTransform());
        if (!data.thumbnailPixmap.isNull()) {
            QLinearGradient scrim(0, r.height()*0.5, 0, r.height());
            scrim.setColorAt(0, Qt::transparent);
            scrim.setColorAt(1, QColor(0, 0, 0, 220));
            p.fillRect(r, scrim);
        }
        if (!data.caption.isEmpty()) {
            QFont f = uiFont(8.5);
            p.setFont(f);
            QFontMetrics fm(f);
            int maxW = r.width() - (data.score > 0.25 ? 75 : 22);
            QString txt = fm.elidedText(data.caption, Qt::ElideRight, maxW);
            int tw = fm.horizontalAdvance(txt) + 16;
            QRect tr(10, r.bottom() - 30, tw, 20);
            p.setPen(Qt::NoPen);
            p.setBrush(QColor(0, 0, 0, 200));
            QPainterPath bp; bp.addRoundedRect(tr, 6, 6);
            p.drawPath(bp);
            p.setPen(Colors::textPrimary);
            p.drawText(tr, Qt::AlignCenter, txt);
        }
        if (data.score > 0.25) {
            QFont f = monoFont(8.5);
            p.setFont(f);
            QFontMetrics fm(f);
            QString sc = QString::number(data.score, 'f', 2);
            int sw = fm.horizontalAdvance(sc) + 16;
            QRect sr(r.right() - 10 - sw, r.bottom() - 30, sw, 20);
            p.setPen(Qt::NoPen);
            p.setBrush(QColor(255, 255, 255, 25));
            QPainterPath bp; bp.addRoundedRect(sr, 6, 6);
            p.drawPath(bp);
            p.setPen(Colors::foreground);
            p.drawText(sr, Qt::AlignCenter, sc);
        }
    }
private:
    ShimmerWidget *_shimmer = nullptr;
    QPropertyAnimation *_hoverAnim = nullptr;
    QGraphicsOpacityEffect *_fadeEff = nullptr;
    HeartBtn *_heart = nullptr;
    qreal _hover = 0.0;
};

// ═══════════════════════════════════════════════════════════════════
//  MASONRY GRID
// ═══════════════════════════════════════════════════════════════════

class MasonryGrid : public QWidget {
    Q_OBJECT
public:
    int colW = ImageCard::BASE_W;
    const int gap = 12;
    QVector<ImageCard*> cards;
    explicit MasonryGrid(QWidget *p = nullptr) : QWidget(p) {
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
    void setColWidth(int w) {
        colW = qMax(80, w);
        for (auto *c : cards) {
            int h = qRound(colW / c->data.aspectRatio);
            c->setFixedSize(colW, qMax(h, 60));
            c->resizeToWidth(colW);
        }
        relayout();
    }
    void relayout() {
        if (cards.isEmpty()) { setFixedHeight(8); return; }
        int W = width();
        int cols = qMax(1, (W + gap) / (colW + gap));
        int gridW = cols * (colW + gap) - gap;
        int xStart = qMax(0, (W - gridW) / 2);
        QVector<int> colH(cols, 8);
        for (auto *c : cards) {
            int col = int(std::min_element(colH.begin(), colH.end()) - colH.begin());
            int x = xStart + col * (colW + gap);
            int y = colH[col];
            c->move(x, y);
            colH[col] += c->height() + gap;
        }
        int maxH = *std::max_element(colH.begin(), colH.end());
        setFixedHeight(maxH + 8);
    }
protected:
    void resizeEvent(QResizeEvent *) override { relayout(); }
};

// ═══════════════════════════════════════════════════════════════════
//  LIGHTBOX
// ═══════════════════════════════════════════════════════════════════

class LightboxOverlay : public QWidget {
    Q_OBJECT
public:
    explicit LightboxOverlay(QWidget *parent) : QWidget(parent) {
        setAttribute(Qt::WA_NoSystemBackground);
        setVisible(false);
        _fadeAnim = new QPropertyAnimation(this, "windowOpacity", this);
        _fadeAnim->setDuration(250);
        _fadeAnim->setEasingCurve(QEasingCurve::OutCubic);
    }
    void showLoading(const QString &caption) {
        _pixmap = QPixmap(); _caption = caption;
        _loading = true; _zoom = 1.0; _offset = QPointF(0,0);
        setVisible(true); raise(); update();
    }
    void showImage(const QPixmap &pix, const QString &caption) {
        _pixmap = pix; _caption = caption;
        _loading = false; _zoom = 1.0; _offset = QPointF(0,0);
        setVisible(true); raise();
        _fadeAnim->stop();
        _fadeAnim->setStartValue(0.0);
        _fadeAnim->setEndValue(1.0);
        _fadeAnim->start();
        update();
    }
    void close() {
        _fadeAnim->stop();
        _fadeAnim->setStartValue(1.0);
        _fadeAnim->setEndValue(0.0);
        connect(_fadeAnim, &QPropertyAnimation::finished, this, [this](){
            setVisible(false);
            disconnect(_fadeAnim, &QPropertyAnimation::finished, nullptr, nullptr);
        });
        _fadeAnim->start();
    }
protected:
    void paintEvent(QPaintEvent *) override {
        QPainter p(this);
        p.setRenderHints(QPainter::Antialiasing | QPainter::SmoothPixmapTransform);
        p.fillRect(rect(), QColor(0, 0, 0, 245));
        if (_loading) {
            p.setFont(uiFont(14));
            p.setPen(Colors::textTertiary);
            p.drawText(rect(), Qt::AlignCenter, "Loading...");
            return;
        }
        if (_pixmap.isNull()) return;
        int captionH = 100;
        QRect imgArea = rect().adjusted(60, 60, -60, -(60 + captionH));
        QSize scaledSz = _pixmap.size().scaled(imgArea.size(), Qt::KeepAspectRatio);
        scaledSz = QSize(int(scaledSz.width() * _zoom), int(scaledSz.height() * _zoom));
        int x = imgArea.center().x() - scaledSz.width()/2 + int(_offset.x());
        int y = imgArea.center().y() - scaledSz.height()/2 + int(_offset.y());
        QPixmap sc = _pixmap.scaled(scaledSz, Qt::KeepAspectRatio, Qt::SmoothTransformation);
        QPainterPath imgClip;
        imgClip.addRoundedRect(QRect(x, y, sc.width(), sc.height()), 16, 16);
        p.setClipPath(imgClip);
        p.drawPixmap(x, y, sc);
        p.setClipping(false);
        if (!_caption.isEmpty()) {
            QFont f = uiFont(12);
            p.setFont(f);
            QFontMetrics fm(f);
            int captionY = rect().bottom() - captionH + 15;
            int maxW = rect().width() - 140;
            QStringList words = _caption.split(' ', Qt::SkipEmptyParts);
            QStringList lines;
            QString cur;
            for (const auto &w : words) {
                QString test = cur.isEmpty() ? w : cur + ' ' + w;
                if (fm.horizontalAdvance(test) > maxW && !cur.isEmpty()) {
                    lines << cur; cur = w;
                    if (lines.size() >= 3) break;
                } else {
                    cur = test;
                }
            }
            if (!cur.isEmpty() && lines.size() < 3) lines << cur;
            if (!lines.isEmpty()) lines.last() = fm.elidedText(lines.last(), Qt::ElideRight, maxW);
            p.setPen(Colors::textSecondary);
            for (int i = 0; i < lines.size(); ++i) {
                QRect lr(70, captionY + i * (fm.height() + 4), rect().width()-140, fm.height()+2);
                p.drawText(lr, Qt::AlignCenter, lines[i]);
            }
        }
        QFont hf = uiFont(9);
        p.setFont(hf);
        p.setPen(Colors::textMuted);
        p.drawText(QRect(0, rect().bottom()-24, rect().width(), 22),
                   Qt::AlignCenter, "Scroll to zoom  ·  Click or Esc to close");
    }
    void mousePressEvent(QMouseEvent *) override { close(); }
    void wheelEvent(QWheelEvent *e) override {
        if (!_pixmap.isNull()) {
            double delta = e->angleDelta().y() / 120.0;
            _zoom = qBound(0.5, _zoom + delta * 0.15, 6.0);
            if (_zoom <= 1.0) _offset = QPointF(0,0);
            update();
        }
    }
    void keyPressEvent(QKeyEvent *e) override {
        if (e->key() == Qt::Key_Escape) close();
    }
private:
    QPixmap _pixmap;
    QString _caption;
    bool _loading = false;
    double _zoom = 1.0;
    QPointF _offset;
    QPropertyAnimation *_fadeAnim;
};

// ═══════════════════════════════════════════════════════════════════
//  TRAFFIC LIGHT DOTS
// ═══════════════════════════════════════════════════════════════════

class WindowControl : public QPushButton {
    Q_OBJECT
public:
    enum Action { Close, Minimize, Maximize };
    WindowControl(Action act, QWidget *p = nullptr)
        : QPushButton(p), _action(act) {
        setFixedSize(28, 28);
        setCursor(Qt::PointingHandCursor);
        setFlat(true);
        setAttribute(Qt::WA_Hover);
    }
signals:
    void triggered(Action);
protected:
    void enterEvent(QEnterEvent *) override { update(); }
    void leaveEvent(QEvent *) override { update(); }
    void mousePressEvent(QMouseEvent *e) override {
        if (e->button() == Qt::LeftButton) emit triggered(_action);
        QPushButton::mousePressEvent(e);
    }
    void paintEvent(QPaintEvent *) override {
        QPainter p(this);
        p.setRenderHint(QPainter::Antialiasing);
        QRect r = rect().adjusted(1, 1, -2, -2);
        QPainterPath path;
        path.addRoundedRect(r, 6, 6);
        QColor bg = underMouse() ? Colors::accent : Colors::secondary;
        p.fillPath(path, bg);
        p.setPen(QPen(Colors::border, 1));
        p.drawPath(path);
        p.setPen(QPen(Colors::mutedForeground, 1.5, Qt::SolidLine, Qt::RoundCap, Qt::RoundJoin));
        const qreal cx = r.center().x();
        const qreal cy = r.center().y();
        const qreal d = 3.5;
        if (_action == Close) {
            p.drawLine(QPointF(cx - d, cy - d), QPointF(cx + d, cy + d));
            p.drawLine(QPointF(cx + d, cy - d), QPointF(cx - d, cy + d));
        } else if (_action == Minimize) {
            p.drawLine(QPointF(cx - d, cy), QPointF(cx + d, cy));
        } else {
            p.setBrush(Qt::NoBrush);
            p.drawRect(QRectF(cx - d, cy - d, d * 2, d * 2));
        }
    }
private:
    Action _action;
};

using TrafficDot = WindowControl;

// ═══════════════════════════════════════════════════════════════════
//  DRAG HANDLE
// ═══════════════════════════════════════════════════════════════════

class DragHandle : public QWidget {
    Q_OBJECT
public:
    explicit DragHandle(QWidget *win, QWidget *p = nullptr) : QWidget(p), _win(win) {}
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
    QWidget *_win; QPoint _dragStart;
};

// ═══════════════════════════════════════════════════════════════════
//  SIDEBAR BUTTON (circular icon + label)
// ═══════════════════════════════════════════════════════════════════

class SidebarBtn : public QPushButton {
    Q_OBJECT
public:
    explicit SidebarBtn(const QString &label, QWidget *p = nullptr)
        : QPushButton(p), _label(label) {
        setCheckable(true); setAutoExclusive(true);
        setSizePolicy(QSizePolicy::Expanding, QSizePolicy::Fixed);
        setFixedHeight(40);
        setCursor(Qt::PointingHandCursor);
        setAttribute(Qt::WA_Hover);
        _hoverAnim = new QVariantAnimation(this);
        _hoverAnim->setDuration(150);
        _hoverAnim->setEasingCurve(QEasingCurve::OutCubic);
        connect(_hoverAnim, &QVariantAnimation::valueChanged,
                [this](const QVariant& v) { _hover = v.toReal(); update(); });
    }
    void setCount(int n) { _count = n; update(); }
    QString label() const { return _label; }
protected:
    void enterEvent(QEnterEvent*) override {
        _hoverAnim->stop();
        _hoverAnim->setStartValue(_hover);
        _hoverAnim->setEndValue(1.0);
        _hoverAnim->start();
    }
    void leaveEvent(QEvent*) override {
        _hoverAnim->stop();
        _hoverAnim->setStartValue(_hover);
        _hoverAnim->setEndValue(0.0);
        _hoverAnim->start();
    }
    void paintEvent(QPaintEvent*) override {
        QPainter p(this);
        p.setRenderHint(QPainter::Antialiasing);
        QRect r = rect().adjusted(4, 2, -4, -2);
        QPainterPath path;
        path.addRoundedRect(r, 8, 8);
        QColor bg = isChecked() ? Colors::accent : Colors::background;
        if (_hover > 0 && !isChecked()) {
            bg = QColor::fromRgbF(
                bg.redF() + (Colors::accent.redF() - bg.redF()) * _hover,
                bg.greenF() + (Colors::accent.greenF() - bg.greenF()) * _hover,
                bg.blueF() + (Colors::accent.blueF() - bg.blueF()) * _hover, 1.0);
        }
        p.fillPath(path, bg);
        if (isChecked()) {
            p.setPen(Qt::NoPen);
            p.setBrush(Colors::foreground);
            p.drawRoundedRect(QRect(r.left() + 2, r.top() + 10, 2, r.height() - 20), 1, 1);
        }
        p.setFont(uiFont(11, isChecked() ? QFont::DemiBold : QFont::Medium));
        p.setPen(isChecked() ? Colors::foreground : Colors::mutedForeground);
        int textX = r.left() + 14;
        int textW = r.width() - 28 - (_count >= 0 ? 48 : 0);
        p.drawText(textX, r.top(), textW, r.height(), Qt::AlignVCenter | Qt::AlignLeft, _label);
        if (_count >= 0) {
            QString countStr = _count > 999 ? "999+" : QString::number(_count);
            QFont bf = monoFont(9);
            p.setFont(bf);
            QFontMetrics fm(bf);
            int bw = fm.horizontalAdvance(countStr) + 12;
            int bh = 20;
            QRect br(r.right() - bw - 6, r.center().y() - bh / 2, bw, bh);
            p.setPen(QPen(QColor(255, 255, 255, 51), 1));
            p.setBrush(QColor(255, 255, 255, 25));
            p.drawRoundedRect(br, bh / 2, bh / 2);
            p.setPen(isChecked() ? Colors::foreground : Colors::mutedForeground);
            p.drawText(br, Qt::AlignCenter, countStr);
        }
    }
private:
    QString _label;
    int _count = -1;
    qreal _hover = 0.0;
    QVariantAnimation* _hoverAnim;
};

class LogoMark : public QWidget {
    Q_OBJECT
public:
    explicit LogoMark(QWidget *p = nullptr) : QWidget(p) { setFixedSize(28, 28); }
protected:
    void paintEvent(QPaintEvent *) override {
        QPainter p(this);
        p.setRenderHint(QPainter::Antialiasing);
        QRect r = rect().adjusted(2, 2, -2, -2);
        QPainterPath path;
        path.addRoundedRect(r, 6, 6);
        p.fillPath(path, Colors::secondary);
        p.setPen(QPen(Colors::border, 1));
        p.drawPath(path);
        p.setPen(Qt::NoPen);
        p.setBrush(Colors::foreground);
        p.drawRoundedRect(r.adjusted(7, 7, -7, -7), 2, 2);
    }
};

class SetupWizard : public QDialog {
    Q_OBJECT
public:
    explicit SetupWizard(QWidget *parent = nullptr) : QDialog(parent) {
        _net = new QNetworkAccessManager(this);
        setWindowFlags(Qt::FramelessWindowHint | Qt::Dialog);
        setAttribute(Qt::WA_TranslucentBackground, false);
        setModal(true);
        setFixedSize(520, 640);
        setStyleSheet("background-color: " + Colors::card.name() + ";");
        auto *layout = new QVBoxLayout(this);
        layout->setContentsMargins(32, 28, 32, 28);
        layout->setSpacing(20);
        auto *title = new QLabel("Welcome to Fotoro", this);
        title->setFont(uiFont(22, QFont::Bold));
        title->setStyleSheet("color: " + Colors::textPrimary.name() + ";");
        layout->addWidget(title, 0, Qt::AlignCenter);
        auto *subtitle = new QLabel("Complete setup once to start using your photo server", this);
        subtitle->setFont(uiFont(12));
        subtitle->setStyleSheet("color: " + Colors::textSecondary.name() + ";");
        layout->addWidget(subtitle, 0, Qt::AlignCenter);
        layout->addSpacing(10);
        _stepLabel = new QLabel("Step 1 of 4", this);
        _stepLabel->setFont(eyebrowFont());
        _stepLabel->setStyleSheet("color: " + Colors::textMuted.name() + ";");
        layout->addWidget(_stepLabel);
        _progress = new QProgressBar(this);
        _progress->setRange(0, 4);
        _progress->setValue(1);
        _progress->setTextVisible(false);
        _progress->setFixedHeight(4);
        _progress->setStyleSheet(R"(
            QProgressBar { background-color: )" + Colors::muted.name() + R"(; border-radius: 2px; }
            QProgressBar::chunk { background-color: )" + Colors::foreground.name() + R"(; border-radius: 2px; }
        )");
        layout->addWidget(_progress);
        layout->addSpacing(10);
        _pages = new QStackedWidget(this);
        _pages->addWidget(createAuthPage());
        _pages->addWidget(createTailscalePage());
        _pages->addWidget(createSchedulePage());
        _pages->addWidget(createDonePage());
        layout->addWidget(_pages, 1);
        auto *btnRow = new QHBoxLayout();
        btnRow->setSpacing(12);
        _backBtn = new CircularButton("Back", CircularButton::Ghost, this);
        _backBtn->setEnabled(false);
        connect(_backBtn, &QPushButton::clicked, this, &SetupWizard::prevStep);
        _nextBtn = new CircularButton("Next", CircularButton::Primary, this);
        _nextBtn->setEnabled(false);
        connect(_nextBtn, &QPushButton::clicked, this, &SetupWizard::nextStep);
        btnRow->addWidget(_backBtn);
        btnRow->addStretch();
        btnRow->addWidget(_nextBtn);
        layout->addLayout(btnRow);

        _authPoll = new QTimer(this);
        _authPoll->setInterval(1200);
        connect(_authPoll, &QTimer::timeout, this, &SetupWizard::pollAuthSession);
        QTimer::singleShot(200, this, &SetupWizard::beginGoogleSignIn);
    }

protected:
    void closeEvent(QCloseEvent *e) override {
        if (!_allowClose) { e->ignore(); return; }
        QDialog::closeEvent(e);
    }
    void reject() override {
        if (!_allowClose) return;
        QDialog::reject();
    }

private slots:
    void beginGoogleSignIn() {
        _authStatus->setText("Opening Google sign-in in your browser…");
        QDesktopServices::openUrl(QUrl(BASE_URL + "/auth/setup"));
        _authPoll->start();
        pollAuthSession();
    }
    void pollAuthSession() {
        auto *reply = _net->get(QNetworkRequest(QUrl(BASE_URL + "/api/auth/session")));
        connect(reply, &QNetworkReply::finished, this, [this, reply]() {
            if (reply->error() != QNetworkReply::NoError) {
                reply->deleteLater();
                return;
            }
            auto obj = QJsonDocument::fromJson(reply->readAll()).object();
            if (!obj.value("authenticated").toBool()) {
                reply->deleteLater();
                return;
            }
            _authPoll->stop();
            _authComplete = true;
            const QString token = obj.value("access_token").toString();
            if (!token.isEmpty()) saveAuthToken(token);
            auto user = obj.value("user").toObject();
            const QString email = user.value("email").toString();
            _authStatus->setText(email.isEmpty()
                ? "Signed in successfully."
                : "Signed in as " + email);
            _authStatus->setStyleSheet("color: " + Colors::foreground.name() + ";");
            _signInBtn->setEnabled(false);
            _nextBtn->setEnabled(true);
            reply->deleteLater();
        });
    }
    void nextStep() {
        if (_currentStep == 0 && !_authComplete) return;
        if (_currentStep < 3) { _currentStep++; updateStep(); }
        else { completeSetup(); }
    }
    void prevStep() {
        if (_currentStep > 0) { _currentStep--; updateStep(); }
    }
    void updateStep() {
        _pages->setCurrentIndex(_currentStep);
        _stepLabel->setText(QString("Step %1 of 4").arg(_currentStep + 1));
        _progress->setValue(_currentStep + 1);
        _backBtn->setEnabled(_currentStep > 0);
        _nextBtn->setText(_currentStep == 3 ? "Finish" : "Next");
        _nextBtn->setEnabled(_currentStep == 0 ? _authComplete : true);
        if (_currentStep == 0 && !_authComplete && !_authPoll->isActive())
            _authPoll->start();
    }
    void completeSetup() {
        QJsonObject body;
        if (_tailscaleKeyInput && !_tailscaleKeyInput->text().trimmed().isEmpty())
            body["tailscale_auth_key"] = _tailscaleKeyInput->text().trimmed();
        if (_timeEdit)
            body["schedule_time"] = _timeEdit->time().toString("HH:mm");
        QStringList days;
        for (int i = 0; i < _dayChecks.size(); ++i) {
            if (_dayChecks[i]->isChecked()) days << QString::number(i);
        }
        if (!days.isEmpty()) body["schedule_days"] = days.join(",");
        QJsonDocument doc(body);
        auto *reply = _net->post(
            apiRequest(QUrl(BASE_URL + "/api/setup/complete")),
            doc.toJson());
        connect(reply, &QNetworkReply::finished, this, [this, reply]() {
            reply->deleteLater();
            QSettings s("fotoro", "fotoro");
            s.setValue("setup/complete", true);
            _allowClose = true;
            accept();
        });
    }

private:
    QWidget* createAuthPage() {
        auto *page = new QWidget(this);
        auto *layout = new QVBoxLayout(page);
        layout->setSpacing(16);
        auto *header = new QLabel("Sign in with Google", page);
        header->setFont(uiFont(14, QFont::DemiBold));
        header->setStyleSheet("color: " + Colors::textPrimary.name() + ";");
        layout->addWidget(header);
        auto *desc = new QLabel(
            "Use Google One Tap or continue with Google in your browser. "
            "This links your account and only runs once for this installation.",
            page);
        desc->setFont(uiFont(11));
        desc->setStyleSheet("color: " + Colors::textSecondary.name() + ";");
        desc->setWordWrap(true);
        layout->addWidget(desc);
        layout->addSpacing(12);
        _signInBtn = new CircularButton("Open Google sign-in", CircularButton::Primary, page);
        connect(_signInBtn, &QPushButton::clicked, this, &SetupWizard::beginGoogleSignIn);
        layout->addWidget(_signInBtn);
        _authStatus = new QLabel("Waiting for sign-in…", page);
        _authStatus->setFont(uiFont(10));
        _authStatus->setStyleSheet("color: " + Colors::textMuted.name() + ";");
        _authStatus->setWordWrap(true);
        layout->addWidget(_authStatus);
        layout->addStretch();
        return page;
    }
    QWidget* createTailscalePage() {
        auto *page = new QWidget(this);
        auto *layout = new QVBoxLayout(page);
        layout->setSpacing(16);
        auto *header = new QLabel("Connect Tailscale", page);
        header->setFont(uiFont(14, QFont::DemiBold));
        header->setStyleSheet("color: " + Colors::textPrimary.name() + ";");
        layout->addWidget(header);
        auto *desc = new QLabel("Tailscale creates a secure VPN between your phone and laptop. Your photos never leave your private network.", page);
        desc->setFont(uiFont(11));
        desc->setStyleSheet("color: " + Colors::textSecondary.name() + ";");
        desc->setWordWrap(true);
        layout->addWidget(desc);
        layout->addSpacing(8);
        _tailscaleKeyInput = createStyledInput("Tailscale Auth Key (optional)");
        layout->addWidget(_tailscaleKeyInput);
        auto *hint = new QLabel("Leave empty to configure later. Get a key from login.tailscale.com", page);
        hint->setFont(uiFont(9));
        hint->setStyleSheet("color: " + Colors::textMuted.name() + ";");
        layout->addWidget(hint);
        layout->addStretch();
        return page;
    }
    QWidget* createSchedulePage() {
        auto *page = new QWidget(this);
        auto *layout = new QVBoxLayout(page);
        layout->setSpacing(16);
        auto *header = new QLabel("Processing Schedule", page);
        header->setFont(uiFont(14, QFont::DemiBold));
        header->setStyleSheet("color: " + Colors::textPrimary.name() + ";");
        layout->addWidget(header);
        auto *desc = new QLabel("Image analysis uses your CPU. Set it to run when you're not using your laptop.", page);
        desc->setFont(uiFont(11));
        desc->setStyleSheet("color: " + Colors::textSecondary.name() + ";");
        desc->setWordWrap(true);
        layout->addWidget(desc);
        layout->addSpacing(8);
        auto *timeRow = new QHBoxLayout();
        auto *timeLabel = new QLabel("Process at:", page);
        timeLabel->setFont(uiFont(11));
        timeLabel->setStyleSheet("color: " + Colors::textSecondary.name() + ";");
        timeRow->addWidget(timeLabel);
        _timeEdit = new QTimeEdit(page);
        _timeEdit->setTime(QTime(2, 0));
        _timeEdit->setDisplayFormat("HH:mm");
        _timeEdit->setFont(uiFont(11));
        _timeEdit->setStyleSheet(R"(
            QTimeEdit { background-color: )" + Colors::bgElevated.name() + R"(;
                color: )" + Colors::textPrimary.name() + R"(;
                border: 1px solid )" + Colors::borderDefault.name() + R"(;
                border-radius: 8px; padding: 8px 12px; }
            QTimeEdit::up-button, QTimeEdit::down-button { width: 0; }
        )");
        timeRow->addWidget(_timeEdit);
        timeRow->addStretch();
        layout->addLayout(timeRow);
        auto *daysLabel = new QLabel("Process on these days:", page);
        daysLabel->setFont(uiFont(11));
        daysLabel->setStyleSheet("color: " + Colors::textSecondary.name() + ";");
        layout->addWidget(daysLabel);
        auto *daysRow = new QHBoxLayout();
        const QStringList days = {"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"};
        for (int i = 0; i < 7; i++) {
            auto *cb = new QCheckBox(days[i], page);
            cb->setChecked(i > 0 && i < 6);
            cb->setFont(uiFont(10));
            cb->setStyleSheet(R"(
                QCheckBox { color: )" + Colors::textSecondary.name() + R"(; }
                QCheckBox::indicator { width: 18px; height: 18px; border-radius: 4px;
                    border: 1px solid )" + Colors::borderDefault.name() + R"(;
                    background: )" + Colors::bgElevated.name() + R"(; }
                QCheckBox::indicator:checked { background: )" + Colors::foreground.name() + R"(;
                    border-color: )" + Colors::foreground.name() + R"(; }
            )");
            daysRow->addWidget(cb);
            _dayChecks.append(cb);
        }
        layout->addLayout(daysRow);
        layout->addStretch();
        return page;
    }
    QWidget* createDonePage() {
        auto *page = new QWidget(this);
        auto *layout = new QVBoxLayout(page);
        layout->setSpacing(20);
        layout->setAlignment(Qt::AlignCenter);
        auto *icon = new QLabel("Done", page);
        icon->setFont(uiFont(12, QFont::DemiBold));
        icon->setStyleSheet("color: " + Colors::mutedForeground.name() + "; letter-spacing: 0.18em;");
        layout->addWidget(icon, 0, Qt::AlignCenter);
        auto *header = new QLabel("You're all set!", page);
        header->setFont(uiFont(16, QFont::Bold));
        header->setStyleSheet("color: " + Colors::textPrimary.name() + ";");
        layout->addWidget(header, 0, Qt::AlignCenter);
        auto *desc = new QLabel("Your Fotoro server is ready. Install the mobile app and start offloading photos.", page);
        desc->setFont(uiFont(11));
        desc->setStyleSheet("color: " + Colors::textSecondary.name() + ";");
        desc->setWordWrap(true);
        desc->setAlignment(Qt::AlignCenter);
        layout->addWidget(desc);
        auto *qrHint = new QLabel("Pairing QR code will appear in Settings", page);
        qrHint->setFont(uiFont(10));
        qrHint->setStyleSheet("color: " + Colors::textMuted.name() + ";");
        layout->addWidget(qrHint, 0, Qt::AlignCenter);
        layout->addStretch();
        return page;
    }
    QLineEdit* createStyledInput(const QString &placeholder) {
        auto *input = new QLineEdit(this);
        input->setPlaceholderText(placeholder);
        input->setFont(uiFont(11));
        input->setFixedHeight(42);
        input->setStyleSheet(R"(
            QLineEdit {
                background-color: )" + Colors::bgElevated.name() + R"(;
                color: )" + Colors::textPrimary.name() + R"(;
                border: 1px solid )" + Colors::borderDefault.name() + R"(;
                border-radius: 10px;
                padding: 0 14px;
                selection-background-color: )" + Colors::accent.name() + R"(;
            }
            QLineEdit:focus {
                border-color: )" + Colors::ring.name() + R"(;
                background-color: )" + Colors::bgCard.name() + R"(;
            }
        )");
        return input;
    }
    int _currentStep = 0;
    bool _authComplete = false;
    bool _allowClose = false;
    QLabel *_stepLabel;
    QProgressBar *_progress;
    QStackedWidget *_pages;
    CircularButton *_backBtn, *_nextBtn, *_signInBtn;
    QLabel *_authStatus;
    QLineEdit *_tailscaleKeyInput;
    QTimeEdit *_timeEdit;
    QVector<QCheckBox*> _dayChecks;
    QTimer *_authPoll = nullptr;
    QNetworkAccessManager *_net = nullptr;
};

// ═══════════════════════════════════════════════════════════════════
//  SETTINGS PANEL
// ═══════════════════════════════════════════════════════════════════

class SettingsPanel : public QFrame {
    Q_OBJECT
public:
    explicit SettingsPanel(QWidget *parent = nullptr) : QFrame(parent) {
        _net = new QNetworkAccessManager(this);
        setFrameStyle(QFrame::NoFrame);
        setStyleSheet("background-color: " + Colors::background.name() + "; border-left: 1px solid " + Colors::border.name() + ";");
        auto *outer = new QVBoxLayout(this);
        outer->setContentsMargins(0, 0, 0, 0);
        outer->setSpacing(0);
        auto *header = new QLabel("Settings", this);
        header->setFont(uiFont(16, QFont::Bold));
        header->setStyleSheet("color: " + Colors::textPrimary.name() + "; padding: 20px 24px 12px 24px;");
        outer->addWidget(header);

        auto *scroll = new QScrollArea(this);
        scroll->setWidgetResizable(true);
        scroll->setHorizontalScrollBarPolicy(Qt::ScrollBarAlwaysOff);
        scroll->setFrameShape(QFrame::NoFrame);
        scroll->setStyleSheet(R"(
            QScrollArea { background: transparent; border: none; }
            QScrollBar:vertical { background: transparent; width: 10px; margin: 0; }
            QScrollBar::handle:vertical { background: )" + Colors::border.name() + R"(;
                min-height: 30px; border-radius: 999px; }
            QScrollBar::handle:vertical:hover { background: )" + Colors::mutedForeground.name() + R"(; }
            QScrollBar::add-line:vertical, QScrollBar::sub-line:vertical { height: 0; border: none; background: none; }
        )");
        auto *scrollBody = new QWidget(scroll);
        auto *layout = new QVBoxLayout(scrollBody);
        layout->setContentsMargins(24, 0, 24, 24);
        layout->setSpacing(20);

        auto *serverCard = createCard("Server");
        _serverStatusLabel = new QLabel("Checking...", serverCard);
        _serverStatusLabel->setFont(uiFont(11));
        serverCard->layout()->addWidget(_serverStatusLabel);
        auto *serverBtns = new QHBoxLayout();
        serverBtns->setSpacing(8);
        _startServerBtn = new CircularButton("Start", CircularButton::Primary, serverCard);
        _stopServerBtn = new CircularButton("Stop", CircularButton::Outline, serverCard);
        serverBtns->addWidget(_startServerBtn);
        serverBtns->addWidget(_stopServerBtn);
        serverBtns->addStretch();
        static_cast<QVBoxLayout*>(serverCard->layout())->addLayout(serverBtns);
        connect(_startServerBtn, &QPushButton::clicked, this, &SettingsPanel::onStartServer);
        connect(_stopServerBtn, &QPushButton::clicked, this, &SettingsPanel::onStopServer);
        layout->addWidget(serverCard);

        auto *memCard = createCard("System Memory");
        _memoryStatusLabel = new QLabel("RAM: --", memCard);
        _memoryStatusLabel->setFont(monoFont(10));
        _memoryStatusLabel->setStyleSheet("color: " + Colors::textTertiary.name() + ";");
        memCard->layout()->addWidget(_memoryStatusLabel);
        layout->addWidget(memCard);

        auto *tsCard = createCard("Tailscale Network");
        _tsStatus = new QLabel("Checking...", tsCard);
        _tsStatus->setFont(uiFont(11));
        tsCard->layout()->addWidget(_tsStatus);
        _tsIP = new QLabel("IP: --", tsCard);
        _tsIP->setFont(monoFont(10));
        _tsIP->setStyleSheet("color: " + Colors::textTertiary.name() + ";");
        tsCard->layout()->addWidget(_tsIP);
        auto *tsBtns = new QHBoxLayout();
        tsBtns->setSpacing(8);
        _connectTailscaleBtn = new CircularButton("Connect", CircularButton::Primary, tsCard);
        _disconnectTailscaleBtn = new CircularButton("Disconnect", CircularButton::Outline, tsCard);
        tsBtns->addWidget(_connectTailscaleBtn);
        tsBtns->addWidget(_disconnectTailscaleBtn);
        tsBtns->addStretch();
        static_cast<QVBoxLayout*>(tsCard->layout())->addLayout(tsBtns);
        connect(_connectTailscaleBtn, &QPushButton::clicked, this, &SettingsPanel::onConnectTailscale);
        connect(_disconnectTailscaleBtn, &QPushButton::clicked, this, &SettingsPanel::onDisconnectTailscale);
        layout->addWidget(tsCard);

        auto *schedCard = createCard("Processing Schedule");
        _schedStatus = new QLabel("Not configured", schedCard);
        _schedStatus->setFont(uiFont(11));
        schedCard->layout()->addWidget(_schedStatus);
        auto *editSchedBtn = new CircularButton("Edit Schedule", CircularButton::Outline, schedCard);
        schedCard->layout()->addWidget(editSchedBtn);
        layout->addWidget(schedCard);

        auto *queueCard = createCard("Upload Queue");
        _queueStatus = new QLabel("0 pending", queueCard);
        _queueStatus->setFont(uiFont(11));
        queueCard->layout()->addWidget(_queueStatus);
        _runSchedulerBtn = new CircularButton("Process Now", CircularButton::Primary, queueCard);
        queueCard->layout()->addWidget(_runSchedulerBtn);
        connect(_runSchedulerBtn, &QPushButton::clicked, this, &SettingsPanel::onRunScheduler);
        layout->addWidget(queueCard);

        auto *pairCard = createCard("Mobile Pairing");
        auto *pairLabel = new QLabel("Scan QR code with your phone to pair", pairCard);
        pairLabel->setFont(uiFont(11));
        pairLabel->setStyleSheet("color: " + Colors::textSecondary.name() + ";");
        pairCard->layout()->addWidget(pairLabel);
        _qrCode = new QLabel(pairCard);
        _qrCode->setFixedSize(180, 180);
        _qrCode->setStyleSheet("background-color: " + Colors::bgElevated.name() + "; border-radius: 12px;");
        _qrCode->setAlignment(Qt::AlignCenter);
        _qrCode->setText("QR");
        _qrCode->setFont(uiFont(10));
        static_cast<QVBoxLayout*>(pairCard->layout())->addWidget(_qrCode, 0, Qt::AlignCenter);
        layout->addWidget(pairCard);
        layout->addStretch();
        scroll->setWidget(scrollBody);
        outer->addWidget(scroll, 1);

        auto *timer = new QTimer(this);
        connect(timer, &QTimer::timeout, this, &SettingsPanel::refreshStatus);
        timer->start(5000);
        refreshStatus();
    }

public slots:
    void refreshStatus() {
        auto *reply = _net->get(QNetworkRequest(QUrl(BASE_URL + "/api/server/status")));
        connect(reply, &QNetworkReply::finished, this, [this, reply]() {
            if (reply->error() == QNetworkReply::NoError) {
                auto obj = QJsonDocument::fromJson(reply->readAll()).object();
                bool running = obj.value("running").toBool();
                _serverStatusLabel->setText(running ? "Running" : "Stopped");
                _serverStatusLabel->setStyleSheet("color: " + Colors::foreground.name() + ";");
                _startServerBtn->setEnabled(!running);
                _stopServerBtn->setEnabled(running);
            }
            reply->deleteLater();
        });

        auto *tsReply = _net->get(QNetworkRequest(QUrl(BASE_URL + "/api/tailscale/status")));
        connect(tsReply, &QNetworkReply::finished, this, [this, tsReply]() {
            if (tsReply->error() == QNetworkReply::NoError) {
                auto obj = QJsonDocument::fromJson(tsReply->readAll()).object();
                bool running = obj.value("running").toBool();
                _tsStatus->setText(running ? "Connected" : "Disconnected");
                _tsStatus->setStyleSheet("color: " + Colors::mutedForeground.name() + ";");
                _connectTailscaleBtn->setEnabled(!running);
                _disconnectTailscaleBtn->setEnabled(running);
            }
            tsReply->deleteLater();
        });

        auto *tsInfoReply = _net->get(QNetworkRequest(QUrl(BASE_URL + "/api/tailscale/info")));
        connect(tsInfoReply, &QNetworkReply::finished, this, [this, tsInfoReply]() {
            if (tsInfoReply->error() == QNetworkReply::NoError) {
                auto obj = QJsonDocument::fromJson(tsInfoReply->readAll()).object();
                QString ip = obj.value("ip").toString();
                _tsIP->setText(ip.isEmpty() ? "IP: --" : "IP: " + ip);
            }
            tsInfoReply->deleteLater();
        });

        auto *schedReply = _net->get(QNetworkRequest(QUrl(BASE_URL + "/api/scheduler/status")));
        connect(schedReply, &QNetworkReply::finished, this, [this, schedReply]() {
            if (schedReply->error() == QNetworkReply::NoError) {
                auto obj = QJsonDocument::fromJson(schedReply->readAll()).object();
                QString status = obj.value("status").toString("idle");
                _schedStatus->setText(status == "idle" ? "Idle" : status);
                int pending = obj.value("pending").toInt();
                _queueStatus->setText(QString("%1 pending").arg(pending));
            }
            schedReply->deleteLater();
        });

        auto *memReply = _net->get(QNetworkRequest(QUrl(BASE_URL + "/api/system/memory")));
        connect(memReply, &QNetworkReply::finished, this, [this, memReply]() {
            if (memReply->error() == QNetworkReply::NoError) {
                auto obj = QJsonDocument::fromJson(memReply->readAll()).object();
                auto mem = obj.value("memory").toObject();
                QString status = QString("RAM: %1/%2 MB").arg(
                    QString::number(mem.value("UsedMB").toInt()),
                    QString::number(mem.value("TotalMB").toInt()));
                _memoryStatusLabel->setText(status);
            }
            memReply->deleteLater();
        });
    }

    void onStartServer() {
        auto *reply = _net->post(QNetworkRequest(QUrl(BASE_URL + "/api/server/start")), QByteArray());
        connect(reply, &QNetworkReply::finished, this, [this, reply]() {
            reply->deleteLater();
            refreshStatus();
        });
    }

    void onStopServer() {
        auto *reply = _net->post(QNetworkRequest(QUrl(BASE_URL + "/api/server/stop")), QByteArray());
        connect(reply, &QNetworkReply::finished, this, [this, reply]() {
            reply->deleteLater();
            refreshStatus();
        });
    }

    void onConnectTailscale() {
        bool ok;
        QString authKey = QInputDialog::getText(this, "Tailscale Auth Key",
            "Enter your Tailscale auth key:", QLineEdit::Password, "", &ok);
        if (!ok || authKey.isEmpty()) return;

        QJsonObject obj;
        obj["auth_key"] = authKey;
        QJsonDocument doc(obj);

        auto *reply = _net->post(
            QNetworkRequest(QUrl(BASE_URL + "/api/tailscale/connect")),
            doc.toJson());
        connect(reply, &QNetworkReply::finished, this, [this, reply]() {
            reply->deleteLater();
            refreshStatus();
        });
    }

    void onDisconnectTailscale() {
        auto *reply = _net->post(QNetworkRequest(QUrl(BASE_URL + "/api/tailscale/disconnect")), QByteArray());
        connect(reply, &QNetworkReply::finished, this, [this, reply]() {
            reply->deleteLater();
            refreshStatus();
        });
    }

    void onRunScheduler() {
        auto *reply = _net->post(QNetworkRequest(QUrl(BASE_URL + "/api/scheduler/run")), QByteArray());
        connect(reply, &QNetworkReply::finished, this, [this, reply]() {
            auto obj = QJsonDocument::fromJson(reply->readAll()).object();
            reply->deleteLater();
            QMessageBox::information(this, "Scheduler",
                obj.value("status").toString("Processing started"));
            refreshStatus();
        });
    }

private:
    QFrame* createCard(const QString &title) {
        auto *card = new QFrame(this);
        card->setStyleSheet(R"(
            QFrame { background-color: )" + Colors::card.name() + R"(;
                border-radius: 12px; border: 1px solid )" + Colors::border.name() + R"(; }
        )");
        auto *layout = new QVBoxLayout(card);
        layout->setContentsMargins(16, 14, 16, 14);
        layout->setSpacing(10);
        auto *lbl = new QLabel(title, card);
        lbl->setFont(uiFont(11, QFont::DemiBold));
        lbl->setStyleSheet("color: " + Colors::textPrimary.name() + ";");
        layout->addWidget(lbl);
        return card;
    }

    QNetworkAccessManager *_net = nullptr;
    CircularButton *_startServerBtn = nullptr;
    CircularButton *_stopServerBtn = nullptr;
    CircularButton *_connectTailscaleBtn = nullptr;
    CircularButton *_disconnectTailscaleBtn = nullptr;
    CircularButton *_runSchedulerBtn = nullptr;
    QLabel *_serverStatusLabel = nullptr;
    QLabel *_memoryStatusLabel = nullptr;
    QLabel *_tsStatus = nullptr;
    QLabel *_tsIP = nullptr;
    QLabel *_schedStatus = nullptr;
    QLabel *_queueStatus = nullptr;
    QLabel *_qrCode = nullptr;
};

// ═══════════════════════════════════════════════════════════════════
//  MAIN WINDOW
// ═══════════════════════════════════════════════════════════════════

class FotoroWindow : public QMainWindow {
    Q_OBJECT
public:
    FotoroWindow() {
        setWindowTitle("fotoro");
        setWindowFlags(Qt::FramelessWindowHint | Qt::Window);
        setAttribute(Qt::WA_TranslucentBackground, false);
        QScreen *scr = QGuiApplication::primaryScreen();
        if (scr) setGeometry(scr->availableGeometry());
        setMinimumSize(1000, 700);
        _net = new QNetworkAccessManager(this);
        QSettings s("fotoro", "fotoro");
        const QStringList favList = s.value("favorites").toStringList();
        _favorites = QSet<QString>(favList.begin(), favList.end());
        auto *central = new QWidget(this);
        setCentralWidget(central);
        central->setObjectName("Central");
        
        // TITLE BAR
        auto *titleBar = new DragHandle(this, central);
        titleBar->setObjectName("TitleBar");
        titleBar->setFixedHeight(56);
        auto *tbL = new QHBoxLayout(titleBar);
        tbL->setContentsMargins(20, 0, 20, 0);
        tbL->setSpacing(12);
        tbL->setAlignment(Qt::AlignVCenter);
        auto *dotsWrap = new QWidget(titleBar);
        dotsWrap->setFixedHeight(28);
        auto *dots = new QHBoxLayout(dotsWrap);
        dots->setSpacing(6);
        dots->setContentsMargins(0, 0, 0, 0);
        dots->setAlignment(Qt::AlignVCenter);
        auto *btnClose = new TrafficDot(TrafficDot::Close, dotsWrap);
        auto *btnMin   = new TrafficDot(TrafficDot::Minimize, dotsWrap);
        auto *btnMax   = new TrafficDot(TrafficDot::Maximize, dotsWrap);
        dots->addWidget(btnClose);
        dots->addWidget(btnMin);
        dots->addWidget(btnMax);
        tbL->addWidget(dotsWrap);
        tbL->addSpacing(8);
        auto *appIcon = new LogoMark(titleBar);
        tbL->addWidget(appIcon, 0, Qt::AlignVCenter);
        auto *appName = new QLabel("fotoro", titleBar);
        appName->setFont(uiFont(15, QFont::DemiBold));
        appName->setStyleSheet("color: " + Colors::foreground.name() + ";");
        tbL->addWidget(appName, 0, Qt::AlignVCenter);
        tbL->addStretch();
        _serverBadge = new QLabel("offline", titleBar);
        _serverBadge->setObjectName("ServerBadge");
        _serverBadge->setFont(monoFont(10));
        _serverBadge->setStyleSheet(pillBadgeStyle());
        tbL->addWidget(_serverBadge, 0, Qt::AlignVCenter);
        _settingsBtn = new CircularIconBtn(IconButton::Settings, titleBar);
        connect(_settingsBtn, &QPushButton::clicked, this, &FotoroWindow::toggleSettings);
        tbL->addWidget(_settingsBtn, 0, Qt::AlignVCenter);
        connect(btnClose, &TrafficDot::triggered, [this](auto){ close(); });
        connect(btnMin,   &TrafficDot::triggered, [this](auto){ showMinimized(); });
        connect(btnMax,   &TrafficDot::triggered, [this](auto){
            isMaximized() ? showNormal() : showMaximized();
        });
        
        // SIDEBAR
        auto *sidebar = new QWidget(central);
        sidebar->setObjectName("Sidebar");
        sidebar->setFixedWidth(248);
        auto *sideLayout = new QVBoxLayout(sidebar);
        sideLayout->setContentsMargins(16, 20, 16, 20);
        sideLayout->setSpacing(4);
        auto *searchContainer = new QFrame(sidebar);
        searchContainer->setStyleSheet(R"(
            QFrame { background-color: )" + Colors::card.name() + R"(;
                border-radius: 12px; border: 1px solid )" + Colors::border.name() + R"(; }
        )");
        auto *searchLayout = new QVBoxLayout(searchContainer);
        searchLayout->setContentsMargins(12, 12, 12, 12);
        searchLayout->setSpacing(10);
        _searchBar = new QLineEdit(searchContainer);
        _searchBar->setFont(uiFont(12));
        _searchBar->setPlaceholderText("Search your library...");
        _searchBar->setFixedHeight(40);
        _searchBar->setStyleSheet(R"(
            QLineEdit { background-color: rgba(0,0,0,0.5); color: )" + Colors::foreground.name() + R"(;
                border: 1px solid )" + Colors::border.name() + R"(;
                border-radius: 6px; padding: 0 12px; }
            QLineEdit:focus { border-color: )" + Colors::ring.name() + R"(; }
            QLineEdit::placeholder { color: )" + Colors::mutedForeground.name() + R"(; }
        )");
        searchLayout->addWidget(_searchBar);
        auto *filterRow = new QHBoxLayout();
        filterRow->setSpacing(8);
        auto *allFilter = new CircularButton("All", CircularButton::Primary, searchContainer);
        allFilter->setCompact(true);
        auto *favFilter = new CircularButton("Saved", CircularButton::Ghost, searchContainer);
        favFilter->setCompact(true);
        filterRow->addWidget(allFilter);
        filterRow->addWidget(favFilter);
        filterRow->addStretch();
        searchLayout->addLayout(filterRow);
        sideLayout->addWidget(searchContainer);
        sideLayout->addSpacing(20);
        auto *catHeader = new QLabel("LIBRARY", sidebar);
        catHeader->setFont(eyebrowFont());
        catHeader->setStyleSheet("color: " + Colors::mutedForeground.name() + "; padding-left: 8px;");
        sideLayout->addWidget(catHeader);
        sideLayout->addSpacing(6);
        struct CatDef { QString label, key; };
        const QVector<CatDef> cats = {
            {"All Photos",   ""},
            {"Favorites",    "__favorites__"},
            {"Timeline",     "timeline"},
            {"People",       "people"},
            {"Places",       "places"},
            {"Events",       "events"},
            {"Screenshots",  "screenshots"},
            {"Documents",    "documents"},
            {"Wallpapers",   "wallpapers"},
        };
        bool firstCat = true;
        for (const auto &c : cats) {
            auto *btn = new SidebarBtn(c.label, sidebar);
            if (firstCat) { btn->setChecked(true); firstCat = false; }
            _catButtons.append(btn);
            sideLayout->addWidget(btn);
            connect(btn, &QPushButton::clicked, [this, c](){
                _activeCategory = c.key;
                _activeQuery = "";
                _searchBar->clear();
                reloadGallery();
            });
        }
        sideLayout->addStretch();
        
        // MAIN CONTENT
        auto *contentArea = new QWidget(central);
        auto *contentLayout = new QVBoxLayout(contentArea);
        contentLayout->setContentsMargins(0, 0, 0, 0);
        contentLayout->setSpacing(0);
        auto *toolbar = new QWidget(contentArea);
        toolbar->setFixedHeight(52);
        auto *toolbarLayout = new QHBoxLayout(toolbar);
        toolbarLayout->setContentsMargins(20, 0, 20, 0);
        toolbarLayout->setSpacing(12);
        _statusLabel = new QLabel("Loading...", toolbar);
        _statusLabel->setFont(uiFont(11, QFont::Medium));
        _statusLabel->setStyleSheet("color: " + Colors::textSecondary.name() + ";");
        toolbarLayout->addWidget(_statusLabel);
        toolbarLayout->addStretch();
        auto *zoomLabel = new QLabel("Size", toolbar);
        zoomLabel->setFont(uiFont(10, QFont::Medium));
        zoomLabel->setStyleSheet("color: " + Colors::mutedForeground.name() + ";");
        toolbarLayout->addWidget(zoomLabel);
        _zoomSlider = new QSlider(Qt::Horizontal, toolbar);
        _zoomSlider->setRange(120, 400);
        _zoomSlider->setValue(240);
        _zoomSlider->setFixedWidth(120);
        _zoomSlider->setStyleSheet(R"(
            QSlider::groove:horizontal { height: 4px; background: )" + Colors::muted.name() + R"(;
                border-radius: 2px; }
            QSlider::handle:horizontal { background: )" + Colors::mutedForeground.name() + R"(;
                width: 12px; height: 12px; margin: -4px 0; border-radius: 6px; }
            QSlider::handle:horizontal:hover { background: )" + Colors::foreground.name() + R"(; }
            QSlider::sub-page:horizontal { background: )" + Colors::foreground.name() + R"(;
                border-radius: 2px; }
        )");
        toolbarLayout->addWidget(_zoomSlider);
        contentLayout->addWidget(toolbar);
        _scrollArea = new QScrollArea(contentArea);
        _scrollArea->setObjectName("Gallery");
        _scrollArea->setWidgetResizable(true);
        _scrollArea->setHorizontalScrollBarPolicy(Qt::ScrollBarAlwaysOff);
        _scrollArea->setVerticalScrollBarPolicy(Qt::ScrollBarAsNeeded);
        _scrollArea->setStyleSheet(R"(
            QScrollArea { background-color: transparent; border: none; }
            QScrollBar:vertical { background: transparent; width: 10px; margin: 0; }
            QScrollBar::handle:vertical { background: )" + Colors::border.name() + R"(;
                min-height: 30px; border-radius: 999px; }
            QScrollBar::handle:vertical:hover { background: )" + Colors::mutedForeground.name() + R"(; }
            QScrollBar::add-line:vertical, QScrollBar::sub-line:vertical {
                border: none; background: none; height: 0; }
        )");
        _masonry = new MasonryGrid();
        _scrollArea->setWidget(_masonry);
        contentLayout->addWidget(_scrollArea, 1);
        
        // SETTINGS PANEL
        _settingsPanel = new SettingsPanel(central);
        _settingsPanel->setFixedWidth(320);
        _settingsPanel->hide();
        
        // LIGHTBOX
        _lightbox = new LightboxOverlay(central);
        _lightbox->installEventFilter(this);
        
        // ROOT LAYOUT
        auto *rootVL = new QVBoxLayout(central);
        rootVL->setContentsMargins(0,0,0,0);
        rootVL->setSpacing(0);
        rootVL->addWidget(titleBar);
        auto *contentRow = new QHBoxLayout();
        contentRow->setContentsMargins(0,0,0,0);
        contentRow->setSpacing(0);
        contentRow->addWidget(sidebar);
        contentRow->addWidget(contentArea, 1);
        contentRow->addWidget(_settingsPanel);
        rootVL->addLayout(contentRow);
        
        // CONNECTIONS
        connect(_searchBar, &QLineEdit::returnPressed, this, &FotoroWindow::executeSearch);
        connect(_zoomSlider, &QSlider::valueChanged, this, &FotoroWindow::onZoom);
        connect(_scrollArea->verticalScrollBar(), &QScrollBar::valueChanged,
                this, &FotoroWindow::checkInfiniteScroll);
        _relayoutTimer = new QTimer(this);
        _relayoutTimer->setSingleShot(true);
        _relayoutTimer->setInterval(55);
        connect(_relayoutTimer, &QTimer::timeout, [this](){ _masonry->relayout(); });
        checkSetup();
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
            if (_lightbox->isVisible()) { _lightbox->close(); return; }
            if (_settingsPanel->isVisible()) { toggleSettings(); return; }
            if (!_searchBar->text().isEmpty()) {
                _searchBar->clear(); reloadGallery(); return;
            }
        }
        if ((e->modifiers() & Qt::ControlModifier) && e->key() == Qt::Key_F) {
            _searchBar->setFocus(); _searchBar->selectAll();
        }
        QMainWindow::keyPressEvent(e);
    }
    bool eventFilter(QObject *obj, QEvent *e) override {
        if (obj == _lightbox && e->type() == QEvent::KeyPress) {
            auto *ke = static_cast<QKeyEvent*>(e);
            if (ke->key() == Qt::Key_Escape) { _lightbox->close(); return true; }
        }
        return QMainWindow::eventFilter(obj, e);
    }
    
private slots:
    void toggleSettings() {
        if (_settingsPanel->isVisible()) {
            auto *anim = new QPropertyAnimation(_settingsPanel, "maximumWidth", this);
            anim->setStartValue(320);
            anim->setEndValue(0);
            anim->setDuration(200);
            anim->setEasingCurve(QEasingCurve::OutCubic);
            connect(anim, &QPropertyAnimation::finished, [this](){
                _settingsPanel->hide();
                _settingsPanel->setMaximumWidth(320);
            });
            anim->start(QAbstractAnimation::DeleteWhenStopped);
        } else {
            _settingsPanel->show();
            _settingsPanel->setMaximumWidth(0);
            auto *anim = new QPropertyAnimation(_settingsPanel, "maximumWidth", this);
            anim->setStartValue(0);
            anim->setEndValue(320);
            anim->setDuration(200);
            anim->setEasingCurve(QEasingCurve::OutCubic);
            anim->start(QAbstractAnimation::DeleteWhenStopped);
        }
    }
    void checkSetup() {
        QSettings local("fotoro", "fotoro");
        if (local.value("setup/complete", false).toBool()) return;
        auto *reply = _net->get(QNetworkRequest(QUrl(BASE_URL + "/api/setup/status")));
        connect(reply, &QNetworkReply::finished, this, [this, reply]() {
            bool showWizard = true;
            if (reply->error() == QNetworkReply::NoError) {
                auto obj = QJsonDocument::fromJson(reply->readAll()).object();
                if (obj.value("setup_complete").toBool()) {
                    QSettings("fotoro", "fotoro").setValue("setup/complete", true);
                    const QString token = loadAuthToken();
                    if (token.isEmpty()) {
                        auto *sess = _net->get(QNetworkRequest(QUrl(BASE_URL + "/api/auth/session")));
                        connect(sess, &QNetworkReply::finished, this, [sess]() {
                            if (sess->error() == QNetworkReply::NoError) {
                                auto o = QJsonDocument::fromJson(sess->readAll()).object();
                                const QString t = o.value("access_token").toString();
                                if (!t.isEmpty()) saveAuthToken(t);
                            }
                            sess->deleteLater();
                        });
                    }
                    showWizard = false;
                }
            }
            reply->deleteLater();
            if (!showWizard) return;
            QTimer::singleShot(400, this, [this]() {
                auto *wizard = new SetupWizard(this);
                wizard->setWindowModality(Qt::ApplicationModal);
                wizard->exec();
            });
        });
    }
    void fetchStats() {
        auto *reply = _net->get(QNetworkRequest(QUrl(BASE_URL + "/api/stats")));
        connect(reply, &QNetworkReply::finished, this, [this, reply](){
            if (reply->error() == QNetworkReply::NoError) {
                auto obj = QJsonDocument::fromJson(reply->readAll()).object();
                int total = obj.value("total").toInt();
                _serverBadge->setText(QString("%1 items").arg(QLocale().toString(total)));
                _serverBadge->setStyleSheet(pillBadgeStyle(Colors::foreground));
                if (!_catButtons.isEmpty())
                    _catButtons[0]->setCount(total);
            }
            reply->deleteLater();
        });
    }
    void onZoom(int v) { _masonry->setColWidth(v); }
    void checkInfiniteScroll(int val) {
        if (_activeQuery.isEmpty() && _activeCategory != "__favorites__" && !_fetching) {
            auto *sb = _scrollArea->verticalScrollBar();
            bool near = (val >= sb->maximum() - 300);
            bool fits = (sb->maximum() == 0);
            if ((near || fits) && _totalFetched > 0 && _totalFetched % 50 == 0) {
                _page++; fetchPage(_page);
            }
        }
    }
    void reloadGallery() {
        _page = 1; _totalFetched = 0;
        _masonry->clearCards();
        if (_activeCategory == "__favorites__") { loadFavorites(); return; }
        fetchPage(_page);
    }
    void loadFavorites() {
        if (_favorites.isEmpty()) { _statusLabel->setText("No favorites yet"); return; }
        _statusLabel->setText("Loading favorites...");
        auto *reply = _net->get(QNetworkRequest(QUrl(BASE_URL + "/api/images?page=1&per_page=200")));
        connect(reply, &QNetworkReply::finished, this, [this, reply](){
            if (reply->error() == QNetworkReply::NoError) {
                auto arr = QJsonDocument::fromJson(reply->readAll()).array();
                int cnt = 0;
                for (const auto &v : arr) {
                    auto obj = v.toObject();
                    QString hash = obj.value("hash").toString();
                    if (_favorites.contains(hash)) {
                        ImageData d;
                        d.hash = hash;
                        d.caption = obj.value("caption").toString();
                        d.category = obj.value("category").toString();
                        d.thumbnailPath = obj.value("thumbnail").toString();
                        d.isFavorite = true;
                        addCard(d); ++cnt;
                    }
                }
                _statusLabel->setText(QString("%1 favorites").arg(cnt));
            }
            reply->deleteLater();
        });
    }
    void fetchPage(int page) {
        if (_fetching) return;
        _fetching = true;
        _statusLabel->setText("Loading...");
        QString url = QString("%1/api/images?page=%2&per_page=50").arg(BASE_URL).arg(page);
        if (!_activeCategory.isEmpty() && _activeCategory != "__favorites__" && _activeCategory != "timeline")
            url += "&category=" + QString::fromUtf8(QUrl::toPercentEncoding(_activeCategory));
        if (_activeCategory == "timeline")
            url += "&sort=date";
        auto *reply = _net->get(QNetworkRequest(QUrl(url)));
        connect(reply, &QNetworkReply::finished, this, [this, reply](){
            _fetching = false;
            if (reply->error() != QNetworkReply::NoError) {
                _statusLabel->setText("Cannot reach fotoro server");
                reply->deleteLater(); return;
            }
            auto arr = QJsonDocument::fromJson(reply->readAll()).array();
            for (const auto &v : arr) {
                auto obj = v.toObject();
                ImageData d;
                d.hash = obj.value("hash").toString();
                d.caption = obj.value("caption").toString();
                d.category = obj.value("category").toString();
                d.thumbnailPath = obj.value("thumbnail").toString();
                d.isFavorite = _favorites.contains(d.hash);
                d.score = 0.0;
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
        _masonry->clearCards();
        _statusLabel->setText("Searching...");
        QString url = QString("%1/api/search?q=%2").arg(BASE_URL).arg(QString::fromUtf8(QUrl::toPercentEncoding(_activeQuery)));
        auto *reply = _net->get(QNetworkRequest(QUrl(url)));
        connect(reply, &QNetworkReply::finished, this, [this, reply](){
            if (reply->error() != QNetworkReply::NoError) {
                _statusLabel->setText("Search failed");
                reply->deleteLater(); return;
            }
            auto doc = QJsonDocument::fromJson(reply->readAll());
            QJsonArray arr = doc.isArray() ? doc.array() : doc.object().value("results").toArray();
            int count = 0;
            for (const auto &v : arr) {
                auto obj = v.toObject();
                ImageData d;
                d.hash = obj.value("hash").toString();
                d.caption = obj.value("caption").toString();
                d.category = obj.value("category").toString();
                d.thumbnailPath = obj.value("thumbnail").toString();
                d.isFavorite = _favorites.contains(d.hash);
                QJsonValue sv = obj.value("score");
                d.score = sv.isString() ? sv.toString().toDouble() : sv.toDouble();
                addCard(d); ++count;
            }
            _totalFetched = count;
            _statusLabel->setText(count == 0 ? "No results" : QString("%1 results").arg(count));
            reply->deleteLater();
        });
    }
    void addCard(const ImageData &d) {
        auto *card = new ImageCard(_masonry);
        card->setCardData(d);
        int initH = qRound(float(_masonry->colW) / 1.333f);
        card->setFixedSize(_masonry->colW, initH);
        card->resizeToWidth(_masonry->colW);
        _masonry->addCard(card);
        connect(card, &ImageCard::clicked, this, &FotoroWindow::onCardClicked);
        connect(card, &ImageCard::favoriteToggled, this, &FotoroWindow::onFavoriteToggled);
        int idx = _masonry->cards.size() - 1;
        card->fadeEffect()->setOpacity(0.0);
        auto *anim = new QPropertyAnimation(card->fadeEffect(), "opacity", card);
        anim->setStartValue(0.0); anim->setEndValue(1.0);
        anim->setDuration(250); anim->setEasingCurve(QEasingCurve::OutCubic);
        QTimer::singleShot(qMin(idx * 12, 300), anim,
            [anim](){ anim->start(QAbstractAnimation::DeleteWhenStopped); });
        if (!d.thumbnailPath.isEmpty())
            fetchThumbnail(card, d.thumbnailPath);
    }
    void fetchThumbnail(ImageCard *card, const QString &path) {
        QPointer<ImageCard> safe = card;
        auto *reply = _net->get(QNetworkRequest(QUrl(BASE_URL + path)));
        connect(reply, &QNetworkReply::finished, this, [this, reply, safe](){
            if (!safe) { reply->deleteLater(); return; }
            if (reply->error() == QNetworkReply::NoError) {
                QPixmap pix;
                if (pix.loadFromData(reply->readAll())) {
                    safe->setThumbnail(pix);
                    if (pix.width() > 0 && pix.height() > 0) {
                        int h = qRound(float(_masonry->colW) * float(pix.height()) / float(pix.width()));
                        h = qBound(80, h, 600);
                        safe->setFixedSize(_masonry->colW, h);
                        safe->resizeToWidth(_masonry->colW);
                        QTimer::singleShot(0, _masonry, [this](){ _masonry->relayout(); });
                    }
                }
            }
            reply->deleteLater();
        });
    }
    void onCardClicked(const ImageData &d) {
        _lightbox->showLoading(d.caption);
        _lightbox->setGeometry(centralWidget()->rect());
        _lightbox->raise();
        auto *reply = _net->get(QNetworkRequest(QUrl(BASE_URL + "/api/image/" + d.hash)));
        connect(reply, &QNetworkReply::finished, this, [this, reply, d](){
            if (reply->error() == QNetworkReply::NoError) {
                QPixmap pix;
                if (pix.loadFromData(reply->readAll()))
                    _lightbox->showImage(pix, d.caption);
            }
            reply->deleteLater();
        });
    }
    void onFavoriteToggled(const QString &hash, bool fav) {
        if (fav) _favorites.insert(hash);
        else     _favorites.remove(hash);
        QSettings s("fotoro", "fotoro");
        s.setValue("favorites", QStringList(_favorites.begin(), _favorites.end()));
        for (auto *card : _masonry->cards) {
            if (card->data.hash == hash)
                card->data.isFavorite = fav;
        }
        _catButtons[1]->setCount(_favorites.size());
    }
private:
    QNetworkAccessManager *_net = nullptr;
    MasonryGrid *_masonry = nullptr;
    QScrollArea *_scrollArea = nullptr;
    QLineEdit *_searchBar = nullptr;
    QSlider *_zoomSlider = nullptr;
    QLabel *_statusLabel = nullptr;
    QLabel *_serverBadge = nullptr;
    CircularIconBtn *_settingsBtn = nullptr;
    SettingsPanel *_settingsPanel = nullptr;
    LightboxOverlay *_lightbox = nullptr;
    QTimer *_relayoutTimer = nullptr;
    QVector<SidebarBtn*> _catButtons;
    QString _activeCategory;
    QString _activeQuery;
    int _page = 1;
    int _totalFetched = 0;
    bool _fetching = false;
    QSet<QString> _favorites;
};

// ═══════════════════════════════════════════════════════════════════
//  STYLESHEET
// ═══════════════════════════════════════════════════════════════════

static const char *QSS = R"(
* {
    font-family: "Geist", "Inter", "Segoe UI", "Helvetica Neue", Arial, sans-serif;
    color-scheme: dark;
}
QMainWindow, QWidget {
    background-color: #000000;
    color: #9E9E9E;
}
QWidget#Central { background-color: #000000; }
QWidget#TitleBar {
    background-color: rgba(0, 0, 0, 0.85);
    border-bottom: 1px solid #242424;
}
QWidget#Sidebar {
    background-color: #000000;
    border-right: 1px solid #242424;
}
QScrollArea#Gallery {
    background-color: #000000;
    border: none;
}
QScrollArea#Gallery > QWidget > QWidget {
    background-color: #000000;
}
QLabel#ServerBadge {
    background-color: rgba(255,255,255,0.06);
    border: 1px solid #242424;
    border-radius: 999px;
    padding: 5px 14px;
    min-height: 22px;
}
QMessageBox, QInputDialog {
    background-color: #0D0D0D;
    color: #F5F5F5;
}
)";

// ═══════════════════════════════════════════════════════════════════
//  ENTRY POINT
// ═══════════════════════════════════════════════════════════════════

int main(int argc, char *argv[]) {
    QApplication app(argc, argv);
    loadFonts();
    app.setStyleSheet(QSS);
    app.setApplicationName("fotoro");
    app.setOrganizationName("fotoro");
    FotoroWindow w;
    w.show();
    return app.exec();
}

#include "main.moc"
